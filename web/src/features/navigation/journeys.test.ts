// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { APPROVALS_SECTION } from '@/components/layout/shell-destinations'
import { JOURNEYS, stepHref, unwalked } from './journeys'
import { viewById } from './model'

describe('the four main journeys', () => {
  it('are first hour, provider keys, sessions and approvals', () => {
    expect(Object.keys(JOURNEYS)).toEqual([
      'firstHour',
      'providerKeys',
      'sessions',
      'approvals',
    ])
  })

  it('name only registered views a link can open', () => {
    for (const steps of Object.values(JOURNEYS)) {
      expect(steps.length).toBeGreaterThan(0)
      for (const step of steps) {
        const view = viewById(step.view)
        expect(view, step.view).toBeDefined()
        expect(view?.path, step.view).not.toContain('$')
      }
    }
  })

  it('resolve every journey link from the same view declarations as the registry', () => {
    for (const steps of Object.values(JOURNEYS)) {
      for (const step of steps) {
        const view = viewById(step.view)!
        const search = new URLSearchParams(
          'search' in step ? step.search : {},
        ).toString()
        expect(stepHref(step)).toBe(
          search ? `${view.path}?${search}` : view.path,
        )
      }
    }
  })

  it('open approvals where the sidebar does', () => {
    expect(JOURNEYS.approvals.at(-1)).toBe(APPROVALS_SECTION)
  })

  // A journey no console spec walks end to end is a table nothing reads.
  it('are each walked to the last step by a console spec', () => {
    const e2e = join(__dirname, '..', '..', '..', 'e2e')
    const specs = readdirSync(e2e)
      .filter((name) => name.endsWith('.spec.ts'))
      .map((name) => readFileSync(join(e2e, name), 'utf8'))
      .join('\n')
      .replace(/\s+/g, ' ')
    const unchecked = Object.keys(JOURNEYS).filter(
      (key) =>
        !specs.includes(
          `expect(unwalked(JOURNEYS.${key}, visited)).toEqual([])`,
        ),
    )
    expect(unchecked).toEqual([])
  })

  it('drive the release first-hour gate', () => {
    const gate = readFileSync(
      join(__dirname, '..', '..', '..', 'e2e-release', 'first-hour.spec.ts'),
      'utf8',
    ).replace(/\s+/g, ' ')
    expect(gate).toContain(
      'expect(unwalked(JOURNEYS.firstHour, visited)).toEqual([])',
    )
  })
})

describe('stepHref', () => {
  it('reads the path from the registry and adds the section search', () => {
    expect(stepHref({ view: 'sessions' })).toBe('/sessions')
    expect(stepHref(APPROVALS_SECTION)).toBe('/permissions?tab=approvals')
  })

  it('adds the caller parameters after the step search', () => {
    expect(
      stepHref(
        { view: 'providers' },
        { add: 'ollama', returnTo: '/onboarding' },
      ),
    ).toBe('/providers?add=ollama&returnTo=%2Fonboarding')
  })

  it('refuses a view the registry does not hold', () => {
    expect(() => stepHref({ view: 'no-such-view' })).toThrow(/no-such-view/)
  })
})

describe('unwalked', () => {
  const base = 'http://127.0.0.1:8456'

  it('is empty when the addresses pass every step in order', () => {
    expect(
      unwalked(JOURNEYS.firstHour, [
        `${base}/setup`,
        `${base}/onboarding`,
        `${base}/providers?add=ollama&returnTo=%2Fonboarding`,
        `${base}/onboarding`,
        `${base}/sessions?session=run%3Aabc`,
      ]),
    ).toEqual([])
  })

  it('returns the steps from the first one never reached', () => {
    expect(
      unwalked(JOURNEYS.firstHour, [`${base}/onboarding`, `${base}/sessions`]),
    ).toEqual(JOURNEYS.firstHour.slice(1))
  })

  it('does not count a step reached out of order', () => {
    expect(
      unwalked(JOURNEYS.firstHour, [
        `${base}/sessions`,
        `${base}/onboarding`,
        `${base}/providers`,
      ]),
    ).toEqual(JOURNEYS.firstHour.slice(2))
  })

  it('requires the section search of a step', () => {
    const approvals = [APPROVALS_SECTION]
    expect(unwalked(approvals, [`${base}/permissions?tab=policies`])).toEqual(
      approvals,
    )
    expect(
      unwalked(approvals, [`${base}/permissions?tab=approvals&approval=a1`]),
    ).toEqual([])
  })
})

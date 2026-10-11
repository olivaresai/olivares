// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import {
  JOURNEYS,
  stepHref,
  unwalked,
} from '../src/features/navigation/journeys'

test('journey links resolve under the Playwright loader', () => {
  expect(JOURNEYS.firstHour.map((step) => stepHref(step))).toEqual([
    '/onboarding',
    '/providers',
    '/sessions',
  ])
  expect(JOURNEYS.providerKeys.map((step) => stepHref(step))).toEqual([
    '/providers',
    '/provider-profiles',
    '/provider-accounts',
  ])
  expect(JOURNEYS.approvals.map((step) => stepHref(step))).toEqual([
    '/sessions',
    '/permissions?tab=approvals',
  ])
  expect(stepHref(JOURNEYS.sessions[0], { session: 'run:abc' })).toBe(
    '/sessions?session=run%3Aabc',
  )
  for (const journey of Object.values(JOURNEYS)) {
    expect(
      unwalked(
        journey,
        journey.map((step) => stepHref(step)),
      ),
    ).toEqual([])
  }
  expect(() => stepHref({ view: 'no-such-view' })).toThrow(/no-such-view/)
})

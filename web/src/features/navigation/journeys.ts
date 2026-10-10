// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FOUR MAIN JOURNEYS, AS REGISTRY VIEWS.
//
// The first job a person does on a fresh install (a key or a local model, a session, an
// approval, stop and resume, history) crosses these four journeys. Each step names a
// registry view and, for a section of a view, the search that opens it. The path and the
// permission are declared by the views themselves, so a step cannot invent a path.
// Those same view records are collected by the registry. The console journey tests (web/e2e) walk these steps instead of typing
// paths, and the release first-hour gate (web/e2e-release) walks the first-hour journey.
import { APPROVALS_SECTION } from '@/components/layout/shell-sections'
import { VIEWS as onboardingViews } from '../onboarding/views'
import { VIEWS as providerViews } from '../providers/views'
import { VIEWS as providerAdminViews } from '../agentops/views'
import { VIEWS as sessionViews } from '../sessions/views'
import { VIEWS as governanceViews } from '../governance/views'
import type { ViewEntry } from '../registry'

// Read the same records Vite collects for the registry, without importing its glob.
// Only these features participate in the four journeys; their pages remain lazy.
const JOURNEY_VIEWS: readonly ViewEntry[] = [
  ...onboardingViews,
  ...providerViews,
  ...providerAdminViews,
  ...sessionViews,
  ...governanceViews,
]

function viewById(id: string): ViewEntry | undefined {
  return JOURNEY_VIEWS.find((view) => view.id === id)
}

export interface JourneyStep {
  /** The registry view id. */
  readonly view: string
  /** The search that opens a section of the view (`?tab=approvals`). */
  readonly search?: Readonly<Record<string, string>>
}

export const JOURNEYS = {
  /** The setup wizard, a key or a local model, then the first session. */
  firstHour: [
    { view: 'onboarding' },
    { view: 'providers' },
    { view: 'sessions' },
  ],
  /** The credential a session launches with, the profile that uses it, and the account a
   * tool signs in with instead of a key. */
  providerKeys: [
    { view: 'providers' },
    { view: 'providerProfiles' },
    { view: 'providerAccounts' },
  ],
  /** Start, talk, stop and resume, and read the history: one room, `?session=` picks one. */
  sessions: [{ view: 'sessions' }],
  /** A waiting session's request, decided in the queue the sidebar opens. */
  approvals: [{ view: 'sessions' }, APPROVALS_SECTION],
} as const satisfies Readonly<Record<string, readonly JourneyStep[]>>

/** The link that opens a step: the registry path, the step's search, then `params`. */
export function stepHref(
  step: JourneyStep,
  params: Readonly<Record<string, string>> = {},
): string {
  const view = viewById(step.view)
  if (!view)
    throw new Error(`journey step names no registered view: ${step.view}`)
  const search = new URLSearchParams({ ...step.search, ...params }).toString()
  return search ? `${view.path}?${search}` : view.path
}

function reaches(step: JourneyStep, address: string): boolean {
  const url = new URL(address, 'http://journey.invalid')
  return (
    url.pathname === viewById(step.view)?.path &&
    Object.entries(step.search ?? {}).every(
      ([key, value]) => url.searchParams.get(key) === value,
    )
  )
}

/**
 * The steps `addresses` (the pages a walk visited, in order) did not reach in order, from
 * the first one missed. Empty means the walk passed every step of the journey.
 */
export function unwalked(
  journey: readonly JourneyStep[],
  addresses: readonly string[],
): JourneyStep[] {
  let next = 0
  for (const address of addresses) {
    if (next < journey.length && reaches(journey[next], address)) next++
  }
  return journey.slice(next)
}

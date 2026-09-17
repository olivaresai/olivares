// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it } from 'vitest'
import { FEATURE_VIEWS } from '@/features/registry'
import { personalLink } from './personal-navigation-store'
import { createSessionRecents, recentDestination } from './session-recents'

it('admits the full registered set without a quota while rejecting details, public paths and entity sheets', () => {
  const session = createSessionRecents(() => true)
  const all = [
    ...FEATURE_VIEWS.filter((view) => personalLink(view.id)).map(
      (view) => view.id,
    ),
    'settings',
  ]
  for (const id of all) {
    session.arrive(id)
    session.admit(id)
  }
  expect(session.getSnapshot().map((link) => link.id)).toEqual(all.toReversed())
  for (const path of [
    '/login',
    '/accept-invite',
    '/status-page',
    '/agentops/entity',
    '/session-viewer/entity',
    '/areas/ai',
    '/not-registered',
  ]) {
    expect(recentDestination(path, '')).toBeNull()
  }
  for (const key of ['channel', 'delivery', 'message', 'admin_channel']) {
    expect(
      recentDestination('/communications', `?${key}=defensive-fixture`),
    ).toBeNull()
    expect(
      recentDestination('/communications/administration', `?${key}=`),
    ).toBeNull()
  }
  expect(recentDestination('/agentops', '?filter=fixture')).toBe('agentops')
  expect(recentDestination('/settings', '')).toBe('settings')
})

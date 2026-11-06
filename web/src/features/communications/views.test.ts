// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE ENTRY FOR COMMUNICATIONS. The five routes still exist (each opens its own tab, on
// its own permission), but navigation offers ONE of them to a person who can open the
// room, so the sidebar, the area directory and the palette say "Communications" once.
import { describe, expect, it } from 'vitest'
import { authorizedSections, permissionGate } from '@/features/navigation/model'
import { VIEWS } from './views'

/** Every communications entry navigation offers to someone holding exactly `held`. */
function offeredTo(held: string[]): string[] {
  const gate = permissionGate((p) => held.includes(p))
  return authorizedSections('work-communications', gate)
    .flatMap((s) => s.views)
    .map((v) => v.id)
    .filter((id) => id.startsWith('communications'))
}

describe('communications navigation', () => {
  it('offers ONE entry to a person who can read the channels, whatever else they hold', () => {
    expect(
      offeredTo([
        'sessions:channel:read',
        'sessions:channel:write',
        'sessions:channel:admin',
        'sessions:delivery:read',
      ]),
    ).toEqual(['communications'])
  })

  it('still offers the inbox to a person who may read deliveries but not the channels', () => {
    expect(offeredTo(['sessions:delivery:read'])).toEqual([
      'communicationsInbox',
      'communicationsHandoffs',
    ])
  })

  it('still offers channel creation to a person who may only create', () => {
    expect(offeredTo(['sessions:channel:write'])).toEqual(['communicationsNew'])
  })

  it('still offers administration to a person who may only administer', () => {
    expect(offeredTo(['sessions:channel:admin'])).toEqual([
      'communicationsAdministration',
    ])
  })

  it('keeps all five routes mounted, each its own door into the one room', () => {
    expect(VIEWS.map((v) => v.path)).toEqual([
      '/communications',
      '/communications/inbox',
      '/communications/new',
      '/communications/handoffs',
      '/communications/administration',
    ])
    expect(
      VIEWS.filter((v) => v.id !== 'communications').map((v) => v.doorTo),
    ).toEqual([
      'communications',
      'communications',
      'communications',
      'communications',
    ])
  })
})

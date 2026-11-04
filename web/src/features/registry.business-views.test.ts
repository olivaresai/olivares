// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Terminal } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'

// A Business build whose extension registers one page and not the page that
// features/<dir>/business-views.tsx shares with the Business source.
vi.mock('./extensions', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./extensions')>()),
  FEATURE_EXTENSIONS: [
    {
      id: 'extension-fixture',
      path: '/extension-fixture',
      hub: 'operate',
      icon: Terminal,
      helpHref: '/',
      navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
      permission: 'fixture:read',
      element: () => null,
    },
  ],
}))

import { BUSINESS_ONLY_VIEW_IDS, FEATURE_VIEWS } from './registry'

const SHARED_BUSINESS_IDS = ['compliance']

describe('Business pages of shared source', () => {
  it('are registered by the Business extension alone: a missing one is not served from Community source', () => {
    expect(BUSINESS_ONLY_VIEW_IDS).toEqual(
      expect.arrayContaining(SHARED_BUSINESS_IDS),
    )
    expect(
      FEATURE_VIEWS.map((view) => view.id).filter((id) =>
        SHARED_BUSINESS_IDS.includes(id),
      ),
    ).toEqual([])
  })
})

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A PAGE THIS PERSON CAN READ BUT NOT CHANGE SAYS SO, once, at the top. It follows the
// registry's `changes` through the engine's effective set; it decides nothing.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { searchStr: string } }) => unknown
  }) => select({ location: { searchStr: '' } }),
}))
const held = vi.hoisted(() => ({ permissions: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    isSuperadmin: false,
    can: (p: string) => held.permissions.has(p),
  }),
}))
vi.mock('@/features/navigation/authorization', () => ({
  useRouteAccess: () => ({ kind: 'permitted', observed: undefined }),
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  PermittedVisit: () => null,
}))

import { FEATURE_VIEWS } from '@/features/registry'
import { RequirePermission } from './require-permission'

const SENTENCE = /You can read this page but not change it\./
const view = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)!
const page = (id: string) =>
  renderIntel(
    <RequirePermission view={view(id)}>
      <p>The screen</p>
    </RequirePermission>,
  )

beforeEach(() => held.permissions.clear())

describe('a page the person can read but not change', () => {
  it('says so above the page, and who can change it', () => {
    held.permissions.add('health:status:read')
    page('health')
    const note = screen.getByText(SENTENCE)
    expect(note).toHaveTextContent('An organization admin can.')
    expect(note.compareDocumentPosition(screen.getByText('The screen'))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('stays silent when the person holds any permission that changes the page', () => {
    held.permissions.add('health:check:write')
    page('health')
    expect(screen.queryByText(SENTENCE)).toBeNull()
  })

  it('stays silent on a page that has nothing to change', () => {
    page('audit')
    expect(screen.queryByText(SENTENCE)).toBeNull()
    expect(screen.getByText('The screen')).toBeInTheDocument()
  })
})

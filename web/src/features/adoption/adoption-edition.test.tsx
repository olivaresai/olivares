// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADOPTION IS BUSINESS: the usage of each person and department is Olivares AI Business
// (COURSE.md, Editions). Below it the view leaves the navigation, its address says the
// edition line in place of the figures, and an administrator finds it as one closed row on
// the Usage page; where the licence holds Business it opens as before.
import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel } from '@/test/intel'

const server = vi.hoisted(() => ({
  answer: { isSuccess: false, data: undefined } as {
    isSuccess: boolean
    data?: Record<string, unknown>
  },
}))
vi.mock('@/lib/hooks/use-server-info', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/hooks/use-server-info')>()),
  useServerInfo: () => server.answer,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    principal: { kind: 'user', user_id: 'u-1', superadmin: true, grants: [] },
    isSuperadmin: true,
    confinedWorkspace: null,
  }),
}))
vi.mock('@/lib/auth/capabilities', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth/capabilities')>()),
  useCapability: () => ({ access: 'unknown', permit: null, question: null }),
}))
vi.mock('@/features/navigation/authorization', async (importOriginal) => ({
  ...(await importOriginal<
    typeof import('@/features/navigation/authorization')
  >()),
  useRouteAccess: () => ({ kind: 'permitted', observed: null }),
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  PermittedVisit: () => null,
}))
const at = vi.hoisted(() => ({ pathname: '/usage' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: at.pathname, searchStr: '' } }),
  useRouter: () => undefined,
}))

import { BusinessRows } from '@/components/layout/business-rows'
import { RequirePermission } from '@/components/layout/require-permission'
import { useViewAccess } from '@/features/navigation/authorization'
import { FEATURE_VIEWS } from '@/features/registry'
import { useModulesStore } from '@/stores/modules'

const ADOPTION = FEATURE_VIEWS.find((v) => v.id === 'adoption')!

/** What server-info says: a Community build, or a commercial one with its licence. */
const community = () => {
  server.answer = { isSuccess: true, data: { edition: 'community' } }
}
const holds = (licensed: string) => {
  server.answer = {
    isSuccess: true,
    data: {
      edition: 'enterprise',
      licensed_edition: licensed,
      license: { status: 'valid' },
    },
  }
}

/** Whether navigation lists Adoption, from a real render of the gate. */
function listed(): boolean {
  let answer: boolean | null = null
  function Probe() {
    answer = useViewAccess().listed(ADOPTION)
    return null
  }
  renderIntel(<Probe />)
  cleanup()
  return answer!
}

function adoptionPage() {
  at.pathname = '/adoption'
  return renderIntel(
    <RequirePermission view={ADOPTION}>
      <p>Adoption figures</p>
    </RequirePermission>,
  )
}

function usageRows() {
  at.pathname = '/usage'
  return renderIntel(<BusinessRows />)
}

beforeEach(() => {
  server.answer = { isSuccess: false, data: undefined }
  useModulesStore.getState().setOff([])
})
afterEach(cleanup)

describe('Adoption in a Community build', () => {
  it('leaves the navigation', () => {
    community()
    expect(listed()).toBe(false)
  })

  it('says the edition line at its address, never the figures', () => {
    community()
    adoptionPage()
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Business.',
    )
    expect(screen.queryByText('Adoption figures')).toBeNull()
  })

  it('is exactly one closed row on the Usage page for an administrator', () => {
    community()
    usageRows()
    const rows = screen.getAllByRole('link', { name: /Adoption/ })
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveAttribute('href', '/adoption')
    expect(rows[0]).toHaveTextContent('Olivares AI Business')
  })
})

describe('Adoption where the licence holds Business', () => {
  it('opens as before: listed, its page, no closed row', () => {
    holds('business')
    expect(listed()).toBe(true)
    adoptionPage()
    expect(screen.getByText('Adoption figures')).toBeInTheDocument()
    cleanup()
    usageRows()
    expect(screen.queryByRole('link', { name: /Adoption/ })).toBeNull()
  })
})

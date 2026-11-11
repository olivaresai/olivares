// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A VIEW PLACED ABOVE THE EDITION HELD HERE (registry `minEdition`). The enterprise overlay
// sets the field on its own views and the shell honours it in its one place: the view
// leaves the navigation; an administrator, who can add a license, finds it as a closed row
// on its place's page; and its address says the edition line in place of the view, to
// anyone who reaches it. No Community view carries the field, so the view here is a fixture
// this test registers beside the real ones.
import { cleanup, screen } from '@testing-library/react'
import i18n from 'i18next'
import type { ReactNode } from 'react'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest'
import type { FeatureView } from '@/features/registry'
import { DEFAULT_AUTH, renderIntel } from '@/test/intel'

vi.mock('@/features/registry', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/registry')>()
  const security = real.FEATURE_VIEWS.find((v) => v.id === 'security')!
  return {
    ...real,
    FEATURE_VIEWS: [
      ...real.FEATURE_VIEWS,
      {
        ...security,
        id: 'editionFixture',
        path: '/edition-fixture',
        permission: undefined,
        changes: undefined,
        minEdition: 'business',
      },
    ],
  }
})

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

const auth = vi.hoisted(() => ({ superadmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    principal: {
      kind: 'user',
      user_id: 'u-1',
      superadmin: auth.superadmin,
      grants: [],
    },
    isSuperadmin: auth.superadmin,
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
const at = vi.hoisted(() => ({ pathname: '/security' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: at.pathname, searchStr: '' } }),
  useRouter: () => undefined,
}))

import { FEATURE_VIEWS } from '@/features/registry'
import { useModulesStore } from '@/stores/modules'
import { useViewAccess } from '@/features/navigation/authorization'
import { BusinessRows } from './business-rows'
import { RequirePermission } from './require-permission'

const view = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)!
const FIXTURE = () => view('editionFixture')
const WAYS_ON =
  'Add a license in Settings > Edition, or see olivares.ai/pricing.'

/** What the build and its license say, as server-info answers them. */
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

/** Whether navigation lists a view for this person, from a real render of the gate. */
function listed(v: FeatureView): boolean {
  let answer: boolean | null = null
  function Probe() {
    answer = useViewAccess().listed(v)
    return null
  }
  renderIntel(<Probe />)
  cleanup()
  return answer!
}

function page(v: FeatureView) {
  at.pathname = v.path
  return renderIntel(
    <RequirePermission view={v}>
      <p>The screen</p>
    </RequirePermission>,
  )
}

function closedRows() {
  at.pathname = '/security'
  return renderIntel(<BusinessRows />)
}

beforeAll(() => {
  i18n.addResource('en', 'nav', 'items.editionFixture', 'Edition fixture')
})
beforeEach(() => {
  auth.superadmin = true
  server.answer = { isSuccess: false, data: undefined }
})
afterEach(() => {
  cleanup()
  useModulesStore.getState().setOff([])
})

describe('a view placed above the edition held here', () => {
  it('leaves the navigation for an administrator and for a member', () => {
    holds('engineer')
    expect(listed(FIXTURE())).toBe(false)
    auth.superadmin = false
    expect(listed(FIXTURE())).toBe(false)
    expect(listed(view('security'))).toBe(true)
  })

  it('is a closed row for an administrator, opening its own address', () => {
    holds('engineer')
    closedRows()
    const row = screen.getByRole('link', {
      name: 'Edition fixture Olivares AI Business',
    })
    expect(row).toHaveAttribute('href', '/edition-fixture')
  })

  it('is no row at all for a member', () => {
    holds('engineer')
    auth.superadmin = false
    closedRows()
    expect(screen.queryByRole('link', { name: /Edition fixture/ })).toBeNull()
  })

  it('says the edition line at its address, with the ways on, to an administrator', () => {
    holds('engineer')
    page(FIXTURE())
    expect(
      screen.getByRole('heading', { name: 'Edition fixture' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(
      `This is part of Olivares AI Business. ${WAYS_ON}`,
    )
    expect(screen.queryByText('The screen')).toBeNull()
  })

  it('says the edition line at its address to a member, who is asked to ask', () => {
    holds('engineer')
    auth.superadmin = false
    page(FIXTURE())
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Business. Ask an administrator.',
    )
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.queryByText('The screen')).toBeNull()
  })

  // Turning its module on could not open it: the edition is said first.
  it('says the edition, not «turn the module on», when its module is off too', () => {
    holds('engineer')
    useModulesStore.getState().setOff(['security'])
    page({ ...FIXTURE(), permission: 'security:finding:read' })
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Business.',
    )
    expect(screen.queryByText(/not enabled|switched off/i)).toBeNull()
  })

  // A continuity boundary is told the view is admitted only when the view itself renders:
  // while the edition line stands in for it, it must not hold or resume what it guards.
  it('tells a continuity boundary the view is not admitted while the line stands in', () => {
    const told: boolean[] = []
    function Boundary({
      admitted,
      children,
    }: {
      admitted: boolean
      children: ReactNode
    }) {
      told.push(admitted)
      return <>{children}</>
    }
    holds('engineer')
    page({ ...FIXTURE(), continuity: Boundary })
    expect(screen.queryByText('The screen')).toBeNull()
    expect(told[told.length - 1]).toBe(false)
    cleanup()
    holds('business')
    page({ ...FIXTURE(), continuity: Boundary })
    expect(screen.getByText('The screen')).toBeInTheDocument()
    expect(told[told.length - 1]).toBe(true)
  })

  it('an unknown licensed value is closed above Community', () => {
    holds('platinum')
    expect(listed(FIXTURE())).toBe(false)
    page(FIXTURE())
    expect(screen.queryByText('The screen')).toBeNull()
  })
})

describe('a view at or below the edition held here', () => {
  it('opens as before: listed, its page, no closed row', () => {
    holds('business')
    expect(listed(FIXTURE())).toBe(true)
    page(FIXTURE())
    expect(screen.getByText('The screen')).toBeInTheDocument()
    cleanup()
    closedRows()
    expect(screen.queryByRole('link', { name: /Edition fixture/ })).toBeNull()
  })

  it('opens while server-info has not answered: the engine is what refuses', () => {
    expect(listed(FIXTURE())).toBe(true)
    page(FIXTURE())
    expect(screen.getByText('The screen')).toBeInTheDocument()
  })

  it('a view with no field opens as today, at any edition', () => {
    holds('platinum')
    expect(listed(view('security'))).toBe(true)
  })
})

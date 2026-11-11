// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A VIEW PLACED ABOVE THE EDITION HELD HERE (registry `minEdition`). The enterprise overlay
// sets the field on its own views and the shell honours it in its one place: the view
// leaves the navigation; an administrator, who can add a license, finds it as a closed row
// on its place's page; and its address says the edition line in place of the view, to
// anyone who reaches it. The view here is a fixture this test registers beside the real
// ones, so the rule is proven apart from any one page's placement.
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
    isPending?: boolean
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

import { CONSOLE_ENTRIES, PLACED_MODULES } from '@/features/module-spec.gen'
import { FEATURE_VIEWS } from '@/features/registry'
import { ApiError } from '@/lib/api/errors'
import { preferenceStorage } from '@/lib/preference-storage'
import { useModulesStore } from '@/stores/modules'
import { useViewAccess } from '@/features/navigation/authorization'
import { BusinessRows } from './business-rows'
import { QueryErrorState } from './query-error-state'
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

// A VIEW THE MODULE SPEC PLACES (core/modulespec `edition`, `reads_below`), as a Community
// build answers: below its edition the gate says the line in place of the page, unless what
// the module stores stays readable and the module runs. Turning a module on opens no
// edition, so «Turn on» is never what stands there.
describe('a view the module spec places above the edition held here', () => {
  const community = () => {
    server.answer = { isSuccess: true, data: { edition: 'community' } }
  }
  const line = (capability: string) =>
    `${capability} is part of Olivares AI Engineer. ${WAYS_ON}`

  it('says the line in place of a view that reads nothing below it, module on or off', () => {
    community()
    page(view('evals'))
    expect(screen.getByRole('heading', { name: 'Evals' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(line('Evals'))
    expect(screen.queryByText('The screen')).toBeNull()
    cleanup()
    useModulesStore.getState().setOff(['evals'])
    page(view('evals'))
    expect(screen.getByRole('status')).toHaveTextContent(line('Evals'))
    expect(screen.queryByText(/not enabled|switched off/i)).toBeNull()
  })

  it('opens a view that reads below its edition on what is stored, while its module runs', () => {
    community()
    page(view('knowledge'))
    expect(screen.getByText('The screen')).toBeInTheDocument()
    expect(screen.queryByText(/is part of Olivares AI/)).toBeNull()
  })

  it('says the line, not «Turn on», where that module is off', () => {
    community()
    useModulesStore.getState().setOff(['knowledge'])
    page(view('knowledge'))
    expect(screen.getByRole('status')).toHaveTextContent(line('Knowledge'))
    expect(screen.queryByText('The screen')).toBeNull()
    expect(screen.queryByText(/not enabled|switched off/i)).toBeNull()
    expect(screen.queryByRole('button', { name: /turn on/i })).toBeNull()
  })

  // The page-level line is the sentence every time: a tag alone reads as an empty page.
  it('says the whole line on every visit, never only its tag', () => {
    community()
    page(view('evals'))
    cleanup()
    page(view('evals'))
    expect(screen.getByRole('status')).toHaveTextContent(line('Evals'))
  })

  // The line names a placed page as the navigation does, never by its module's Settings name.
  it('names each placed page as the navigation does', () => {
    community()
    useModulesStore.getState().setOff(Object.keys(PLACED_MODULES))
    const pages = Object.entries(CONSOLE_ENTRIES).flatMap(([module, ids]) =>
      Object.hasOwn(PLACED_MODULES, module) && ids.length ? [ids[0]] : [],
    )
    expect(pages.length).toBeGreaterThan(0)
    for (const id of pages) {
      page(view(id))
      expect(screen.getByRole('status')).toHaveTextContent(
        `${i18n.t(`nav:items.${id}`)} is part of Olivares AI`,
      )
      cleanup()
    }
  })

  // No flash of «Turn on» for a place that may close: it waits for the edition held here.
  it('waits while server-info has not answered and that module is off', () => {
    server.answer = { isSuccess: false, isPending: true, data: undefined }
    useModulesStore.getState().setOff(['knowledge'])
    page(view('knowledge'))
    // One live region: a screen reader says the wait once.
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status')).toHaveTextContent('Checking access…')
    expect(screen.queryByText('The screen')).toBeNull()
    expect(screen.queryByText(/not enabled|switched off/i)).toBeNull()
  })

  // The engine refuses what the license already holds (an engine or license fault): the gate
  // stays quiet and the read's refusal says the line, then its edition tag (told once, as
  // every refused read): never a blank page.
  it('lets a refused read say its line where the license holds the view', () => {
    preferenceStorage.attach(null, {})
    holds('engineer')
    const refused = new ApiError(
      403,
      'edition_required',
      'Knowledge is part of Olivares AI Engineer.',
      'req-1',
      { edition: 'engineer', capability: 'knowledge' },
    )
    const visit = () => {
      at.pathname = view('knowledge').path
      return renderIntel(
        <RequirePermission view={view('knowledge')}>
          <QueryErrorState error={refused} />
        </RequirePermission>,
      )
    }
    visit()
    expect(screen.getByRole('status')).toHaveTextContent(line('Knowledge'))
    cleanup()
    visit()
    expect(screen.getByText('Olivares AI Engineer')).toBeInTheDocument()
    expect(screen.queryByText(/is part of/)).toBeNull()
  })
})

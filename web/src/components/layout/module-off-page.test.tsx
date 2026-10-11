// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGE OF A VIEW WHOSE MODULE IS OFF IS A CALM ONE: the view's icon, its name, one sentence
// of what it does, and one action. A person who may change modules gets "Turn on", which uses
// the same selection as Settings > Edition & modules and brings the view back without a
// reload; anyone else is told whom to ask. When turning it on takes more than this one module
// the page says so in one line and links to Settings instead of acting. It is never a refusal.
import { act } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { renderIntel, screen, waitFor, within } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { searchStr: string } }) => unknown
  }) => select({ location: { searchStr: '' } }),
  Link: ({
    children,
    to,
    search,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
  }) => (
    <a href={search ? `${to}?${new URLSearchParams(search)}` : to} {...props}>
      {children}
    </a>
  ),
}))
const auth = vi.hoisted(() => ({ isSuperadmin: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...auth, activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
const route = vi.hoisted(() => ({ kind: 'permitted' as string }))
vi.mock('@/features/navigation/authorization', () => ({
  useRouteAccess: () => ({ kind: route.kind, observed: undefined }),
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  PermittedVisit: () => null,
}))
const restart = vi.hoisted(() => ({ resolve: (_ok: boolean) => {} }))
vi.mock('@/features/console/engine-restart', () => ({
  waitForEngineRestart: () =>
    new Promise<boolean>((r) => {
      restart.resolve = r
    }),
}))

import { FEATURE_VIEWS } from '@/features/registry'
import {
  modulesApi,
  type ModuleSelection,
  type ModuleState,
} from '@/features/settings/modules-settings'
import { useModulesStore } from '@/stores/modules'
import { RequirePermission } from './require-permission'

const deploy = FEATURE_VIEWS.find((v) => v.id === 'deploy')!

const catalog = (extra: ModuleState[] = []): ModuleSelection => ({
  modules: [
    { name: 'sessions', selected: true, running: true, always_on: true },
    { name: 'deploy', selected: false, running: false },
    ...extra,
  ],
  running_sessions: 0,
})

beforeEach(() => {
  vi.restoreAllMocks()
  auth.isSuperadmin = false
  route.kind = 'permitted'
  useModulesStore.getState().setOff(['deploy'])
})
afterEach(() => useModulesStore.getState().setOff([]))

const page = () =>
  renderIntel(
    <RequirePermission view={deploy}>
      <p>Deployments screen</p>
    </RequirePermission>,
  )

describe('the page of a view whose module is off', () => {
  it('is the view’s icon, its name, one sentence of what it does, and no refusal', async () => {
    page()
    const heading = screen.getByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Deploy')
    expect(
      screen.getByText('Provision and wire agents to infrastructure'),
    ).toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="module-not-enabled"] svg'),
    ).not.toBeNull()
    expect(screen.queryByText(/not enabled on this installation/i)).toBeNull()
    expect(screen.queryByText('Deployments screen')).toBeNull()
  })

  it('asks a person who may not change modules to ask an administrator', () => {
    page()
    expect(
      screen.getByText('Ask an administrator to turn it on.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('offers one primary Turn on to an administrator, which uses the module selection', async () => {
    auth.isSuperadmin = true
    vi.spyOn(modulesApi, 'get').mockResolvedValue(catalog())
    const select = vi
      .spyOn(modulesApi, 'select')
      .mockResolvedValue({ modules: [], restarting: false })
    page()
    const turnOn = await screen.findByRole('button', { name: 'Turn on' })
    expect(screen.getAllByRole('button')).toHaveLength(1)
    await userEvent.click(turnOn)
    await waitFor(() => expect(select).toHaveBeenCalledWith(['deploy']))
  })

  it('renders the view without a reload once the engine is back with the module on', async () => {
    auth.isSuperadmin = true
    vi.spyOn(modulesApi, 'get').mockResolvedValue(catalog())
    vi.spyOn(modulesApi, 'select').mockResolvedValue({
      modules: [],
      restarting: true,
    })
    page()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Turn on' }),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    await act(async () => restart.resolve(true))
    // The shell re-reads server-info and the module leaves the off list.
    act(() => useModulesStore.getState().setOff([]))
    expect(screen.getByText('Deployments screen')).toBeInTheDocument()
  })

  it('says in one line that more must come on and links to Settings instead of acting', async () => {
    auth.isSuperadmin = true
    vi.spyOn(modulesApi, 'get').mockResolvedValue({
      modules: [
        { name: 'sessions', selected: true, running: true, always_on: true },
        {
          name: 'deploy',
          selected: false,
          running: false,
          requires: ['inventory'],
        },
        { name: 'inventory', selected: false, running: false },
      ],
      running_sessions: 0,
    })
    const select = vi.spyOn(modulesApi, 'select')
    page()
    const note = await screen.findByText(
      'Turning this on also turns on Inventory and restarts the engine.',
    )
    expect(note).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Turn on' })).toBeNull()
    const link = within(note.parentElement!).getByRole('link', {
      name: 'Open Edition & modules',
    })
    expect(link).toHaveAttribute('href', '/settings?section=edition')
    expect(select).not.toHaveBeenCalled()
  })

  it('names the wait for the module list to a screen reader', () => {
    auth.isSuperadmin = true
    vi.spyOn(modulesApi, 'get').mockReturnValue(new Promise(() => {}))
    page()
    expect(screen.getByRole('status', { name: 'Loading…' })).toBeInTheDocument()
  })

  it('gives a refusal precedence: a person who may not open the view learns nothing of its module', () => {
    route.kind = 'forbidden'
    auth.isSuperadmin = true
    page()
    expect(
      screen.queryByText('Provision and wire agents to infrastructure'),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: 'Turn on' })).toBeNull()
    expect(screen.queryByText('Ask an administrator to turn it on.')).toBeNull()
    expect(
      screen.getByText(/You do not have permission to view this/),
    ).toBeInTheDocument()
  })

  it('waits for an answer that is not established before it says a module is off', () => {
    route.kind = 'checking'
    page()
    expect(screen.queryByText('Ask an administrator to turn it on.')).toBeNull()
    expect(
      screen.queryByText('Provision and wire agents to infrastructure'),
    ).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-checking"]'),
    ).not.toBeNull()
  })

  it('offers no action for the communication plane, which no switch turns on', () => {
    useModulesStore.getState().setOff(['communication'])
    auth.isSuperadmin = true
    const inbox = FEATURE_VIEWS.find((v) => v.id === 'communicationsInbox')!
    renderIntel(
      <RequirePermission view={inbox}>
        <p>Inbox screen</p>
      </RequirePermission>,
    )
    expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
    expect(
      screen.getByText('It is not set up on this installation yet.'),
    ).toBeInTheDocument()
  })
})

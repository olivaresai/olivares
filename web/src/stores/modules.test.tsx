// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor } from '@/test/intel'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import { CONSOLE_ENTRIES } from '@/features/module-spec.gen'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import {
  moduleEnabled,
  moduleOfPermission,
  moduleOfView,
  useModulesStore,
} from './modules'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { searchStr: string } }) => unknown
  }) => select({ location: { searchStr: '' } }),
}))
const auth = vi.hoisted(() => ({ isSuperadmin: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...auth, activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
vi.mock('@/features/navigation/authorization', () => ({
  useRouteAccess: () => ({ kind: 'permitted', observed: undefined }),
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  PermittedVisit: () => null,
}))

import { RequirePermission } from '@/components/layout/require-permission'

afterEach(() => useModulesStore.getState().setOff([]))

describe('modules not enabled on this installation (ARCH C1)', () => {
  it('names a permission’s module by its first segment, in one spelling', () => {
    expect(moduleOfPermission('finops:spend:read')).toBe('finops')
    expect(moduleOfPermission(undefined)).toBeUndefined()
    useModulesStore.getState().setOff(['finops', 'Inventory'])
    const off = useModulesStore.getState().off
    expect(moduleEnabled(off, 'finops:spend:read')).toBe(false)
    expect(moduleEnabled(off, 'inventory:catalog:read')).toBe(false)
    expect(moduleEnabled(off, 'sessions:run:write')).toBe(true)
    // A view with no permission belongs to no module, so it is never hidden.
    expect(moduleEnabled(off, undefined)).toBe(true)
    // An edition view the built-in modules do not name follows its permission.
    expect(moduleEnabled(off, 'finops:spend:read', 'editionView')).toBe(false)
  })

  it('the Claude policy console follows its module, not its governance permission', () => {
    useModulesStore.getState().setOff(['claude-policy'])
    const off = useModulesStore.getState().off
    expect(
      moduleEnabled(off, 'governance:claude-policy:read', 'claudePolicy'),
    ).toBe(false)
    expect(moduleEnabled(off, 'governance:identity:read', 'identity')).toBe(
      true,
    )
  })

  it('the Observability page follows its module, not its health permission', () => {
    useModulesStore.getState().setOff(['observability'])
    const off = useModulesStore.getState().off
    expect(moduleEnabled(off, 'health:status:read', 'observability')).toBe(
      false,
    )
    expect(moduleEnabled(off, 'health:status:read', 'health')).toBe(true)
  })

  it('the identity page follows governance; the identity module gates only its panels', () => {
    useModulesStore.getState().setOff(['identity'])
    const off = useModulesStore.getState().off
    expect(moduleEnabled(off, 'governance:identity:read', 'identity')).toBe(
      true,
    )
  })

  it('hides the K3 communication screens when the plane is not effective', () => {
    useModulesStore.getState().setOff(['communication'])
    const off = useModulesStore.getState().off
    expect(
      moduleEnabled(off, 'sessions:delivery:read', 'communicationsInbox'),
    ).toBe(false)
    // The same permission on a sessions screen stays: sessions always runs.
    expect(moduleEnabled(off, 'sessions:delivery:read', 'sessions')).toBe(true)
    expect(
      FEATURE_VIEWS.filter(
        (v) => moduleOfView(v.permission, v.id) === 'communication',
      ).map((v) => v.id),
    ).toEqual([
      'communications',
      'communicationsInbox',
      'communicationsNew',
      'communicationsHandoffs',
      'communicationsAdministration',
    ])
  })

  // The module spec names each Community view's module once (core/modulespec
  // console_entries); the permission fallback is left for edition views it cannot name.
  it('every Community view of a built-in module takes its module from the spec', () => {
    const named = new Set(Object.values(CONSOLE_ENTRIES).flat())
    expect(
      [...named].filter((id) => !FEATURE_VIEWS.some((v) => v.id === id)),
    ).toEqual([])
    const unnamed = FEATURE_VIEWS.filter(
      (v) =>
        !FEATURE_EXTENSIONS.includes(v) &&
        !named.has(v.id) &&
        Object.hasOwn(CONSOLE_ENTRIES, moduleOfPermission(v.permission) ?? ''),
    ).map((v) => v.id)
    expect(unnamed).toEqual([])
  })

  it('absent from server-info means every module runs', () => {
    useModulesStore.getState().setOff(undefined)
    expect(useModulesStore.getState().off.size).toBe(0)
  })

  it('a page of a module that is not enabled says so plainly, never an error', () => {
    useModulesStore.getState().setOff(['finops'])
    const view = {
      id: 'finops',
      permission: 'finops:spend:read',
    } as FeatureView
    renderIntel(
      <RequirePermission view={view}>
        <p>Spend screen</p>
      </RequirePermission>,
    )
    expect(
      screen.getByText('Token cost, budgets and spend'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/not enabled on this installation/i)).toBeNull()
    expect(screen.queryByText('Spend screen')).toBeNull()
  })

  // The notice stands in for the whole view, so it carries the page's one h1. On an installation
  // without the communication plane, the five communication pages had none (accessibility run).
  it('a page of a module that is not enabled keeps its one heading', () => {
    useModulesStore.getState().setOff(['finops'])
    const view = {
      id: 'finops',
      permission: 'finops:spend:read',
    } as FeatureView
    renderIntel(
      <RequirePermission view={view}>
        <p>Spend screen</p>
      </RequirePermission>,
    )
    const headings = screen.getAllByRole('heading', { level: 1 })
    expect(headings).toHaveLength(1)
    expect(headings[0]).toHaveTextContent('Cost & FinOps')
  })

  it('a permitted page brings its own heading; the gate adds none', () => {
    useModulesStore.getState().setOff(undefined)
    const view = {
      id: 'finops',
      permission: 'finops:spend:read',
    } as FeatureView
    renderIntel(
      <RequirePermission view={view}>
        <p>Spend screen</p>
      </RequirePermission>,
    )
    expect(screen.getByText('Spend screen')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
  })

  it('an administrator turns the module on from its page (EU)', async () => {
    const { modulesApi } = await import('@/features/settings/modules-settings')
    vi.spyOn(modulesApi, 'get').mockResolvedValue({
      modules: [
        { name: 'sessions', selected: true, running: true, always_on: true },
        { name: 'consoleviews', selected: true, running: true },
        { name: 'finops', selected: false, running: false },
      ],
      running_sessions: 0,
    })
    const select = vi
      .spyOn(modulesApi, 'select')
      .mockResolvedValue({ modules: [], restarting: false })
    auth.isSuperadmin = true
    useModulesStore.getState().setOff(['finops'])
    const view = {
      id: 'finops',
      permission: 'finops:spend:read',
    } as FeatureView
    renderIntel(
      <RequirePermission view={view}>
        <p>Spend screen</p>
      </RequirePermission>,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Turn on' }),
    )
    await waitFor(() =>
      expect(select).toHaveBeenCalledWith(['consoleviews', 'finops']),
    )
    auth.isSuperadmin = false
  })

  it('the same page renders when its module runs', () => {
    const view = {
      id: 'finops',
      permission: 'finops:spend:read',
    } as FeatureView
    renderIntel(
      <RequirePermission view={view}>
        <p>Spend screen</p>
      </RequirePermission>,
    )
    expect(screen.getByText('Spend screen')).toBeInTheDocument()
  })
})

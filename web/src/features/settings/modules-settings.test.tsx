// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { consoleApi } from '@/features/console/api'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
const restart = vi.hoisted(() => ({ resolve: (_ok: boolean) => {} }))
vi.mock('@/features/console/engine-restart', () => ({
  waitForEngineRestart: () =>
    new Promise<boolean>((r) => {
      restart.resolve = r
    }),
}))

import {
  ModulesSettings,
  moduleRowState,
  modulesApi,
  type ModuleSelection,
} from './modules-settings'

const catalog: ModuleSelection = {
  modules: [
    { name: 'sessions', selected: true, running: true, always_on: true },
    { name: 'finops', selected: true, running: true },
    { name: 'inventory', selected: false, running: false },
    {
      name: 'eventing',
      selected: false,
      running: true,
      required_by: ['orchestration'],
    },
    { name: 'health', selected: true, running: false },
  ],
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(modulesApi, 'get').mockResolvedValue(structuredClone(catalog))
})

describe('Settings > Modules (ARCH C1)', () => {
  it('says each module’s state from what is chosen and what runs', () => {
    expect(catalog.modules.map(moduleRowState)).toEqual([
      'alwaysOn',
      'on',
      'off',
      'keptOn',
      'needsRestart',
    ])
  })

  it('lists every module with a line, a switch and its state; always-on cannot be turned off', async () => {
    renderIntel(<ModulesSettings />)
    const list = await screen.findByRole('list')
    const sessions = within(list).getByRole('switch', { name: 'Sessions' })
    expect(sessions).toBeChecked()
    expect(sessions).toBeDisabled()
    expect(
      within(list).getByText('Spend, budgets and forecasts.'),
    ).toBeVisible()
    expect(within(list).getByText('Needs restart')).toBeInTheDocument()
    expect(
      within(list).getByText('Kept on: needed by Orchestration.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /apply/i })).toBeDisabled()
  })

  it('applies the chosen set, waits for the restart, then reads the result', async () => {
    const select = vi
      .spyOn(modulesApi, 'select')
      .mockResolvedValue({ ...structuredClone(catalog), restarting: true })
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    expect(
      within(
        screen.getByRole('switch', { name: 'Inventory' }).closest('li')!,
      ).getByText('On after Apply'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('switch', { name: 'Cost' }))
    await user.click(screen.getByRole('button', { name: 'Apply 2 changes' }))
    await waitFor(() =>
      expect(select).toHaveBeenCalledWith(['inventory', 'health']),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    const reads = vi.mocked(modulesApi.get).mock.calls.length
    await act(async () => restart.resolve(true))
    await waitFor(() =>
      expect(vi.mocked(modulesApi.get).mock.calls.length).toBeGreaterThan(
        reads,
      ),
    )
  })

  it('a module that holds data says what stops when it is off (holds_data)', async () => {
    vi.spyOn(modulesApi, 'get').mockResolvedValue({
      modules: [
        { name: 'finops', selected: true, running: true, holds_data: true },
        { name: 'voice', selected: false, running: false, holds_data: true },
        { name: 'security', selected: true, running: false, holds_data: true },
        { name: 'deploy', selected: false, running: false },
        {
          name: 'compliance',
          selected: false,
          running: true,
          holds_data: true,
          required_by: ['reporting'],
        },
      ],
    })
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    const voice = (
      await screen.findByRole('switch', { name: 'Voice' })
    ).closest('li')!
    expect(
      within(voice).getByText(
        'Its data is kept, but nothing uses it while the module is off.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText('Findings are no longer collected.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Budgets are no longer enforced.')).toBeNull()
    await user.click(screen.getByRole('switch', { name: 'Cost' }))
    expect(
      screen.getByText('Budgets are no longer enforced.'),
    ).toBeInTheDocument()
    const deploy = screen.getByRole('switch', { name: 'Deploy' }).closest('li')!
    expect(within(deploy).queryByText(/no longer|nothing uses/)).toBeNull()
    // Kept on by Reports: it runs, so its data is still used (08b capture).
    const compliance = screen
      .getByRole('switch', { name: 'Compliance' })
      .closest('li')!
    expect(within(compliance).getByText('Kept on')).toBeInTheDocument()
    expect(
      within(compliance).queryByText('Controls are no longer monitored.'),
    ).toBeNull()
  })

  it('a module an active add-on runs is locked on and says which add-on (activated_by)', async () => {
    // The add-on's name comes from the activation catalog (synthetic title here).
    vi.spyOn(consoleApi, 'getActivation').mockResolvedValue({
      edition: 'business',
      restart_required: false,
      addons: [
        {
          key: 'addon_airs',
          title: 'Synthetic add-on title',
          summary: '',
          env: '',
          preset: 'regulated',
          state: 'active',
        },
      ],
      presets: [],
    })
    vi.spyOn(modulesApi, 'get').mockResolvedValue({
      modules: [
        {
          name: 'inferenceproxy',
          selected: false,
          running: true,
          holds_data: true,
          activated_by: ['addon_airs'],
        },
        { name: 'finops', selected: true, running: true },
      ],
    })
    renderIntel(<ModulesSettings />)
    const proxy = await screen.findByRole('switch', { name: 'Inference proxy' })
    expect(proxy).toBeChecked()
    expect(proxy).toBeDisabled()
    const row = proxy.closest('li')!
    expect(
      await within(row).findByText(
        'Enabled by Synthetic add-on title. It stays on while that add-on is active.',
      ),
    ).toBeInTheDocument()
    expect(within(row).getByText('Add-on')).toBeInTheDocument()
    expect(within(row).queryByText('On after Apply')).toBeNull()
    expect(within(row).queryByText(/no longer|nothing uses/)).toBeNull()
    expect(screen.getByRole('button', { name: /apply/i })).toBeDisabled()
  })

  it('an engine without module selection says so plainly', async () => {
    vi.spyOn(modulesApi, 'get').mockRejectedValue(
      new ApiError(501, 'not_implemented', 'not wired'),
    )
    renderIntel(<ModulesSettings />)
    expect(
      await screen.findByText('Modules cannot be chosen on this engine.'),
    ).toBeInTheDocument()
  })
})

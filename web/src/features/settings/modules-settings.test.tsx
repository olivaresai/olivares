// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import {
  createTestQueryClient,
  renderIntel,
  screen,
  waitFor,
  within,
} from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api'
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
  TurnOnModule,
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
  running_sessions: 0,
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
    expect(
      screen.getByRole('button', { name: /apply/i }),
    ).toHaveAccessibleDescription(
      'No changes to apply. Turn a module on or off first.',
    )
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
    expect(
      screen.getByRole('button', { name: /apply/i }),
    ).toHaveAccessibleDescription('Restarting the engine…')
    const reads = vi.mocked(modulesApi.get).mock.calls.length
    await act(async () => restart.resolve(true))
    await waitFor(() =>
      expect(vi.mocked(modulesApi.get).mock.calls.length).toBeGreaterThan(
        reads,
      ),
    )
  })

  it('warns with the running-session count, read at Apply, before the restart stops them (#507)', async () => {
    const select = vi
      .spyOn(modulesApi, 'select')
      .mockResolvedValue({ ...structuredClone(catalog), restarting: true })
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    // Two sessions started after the page was read: Apply reads the count again.
    vi.mocked(modulesApi.get).mockResolvedValue({
      ...structuredClone(catalog),
      running_sessions: 2,
    })
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        '2 running sessions stop. Each one can be resumed after the restart.',
      ),
    ).toBeInTheDocument()
    expect(select).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: 'Restart and apply' }),
    )
    await waitFor(() =>
      expect(select).toHaveBeenCalledWith(['finops', 'inventory', 'health']),
    )
  })

  it('warns for a change only the engine knows restarts it (#507)', async () => {
    // Health waits for a restart, so the engine restarts for any Apply, even one
    // that only chooses Eventing, which Orchestration already keeps on.
    vi.mocked(modulesApi.get).mockResolvedValue({
      ...structuredClone(catalog),
      running_sessions: 2,
    })
    const select = vi
      .spyOn(modulesApi, 'select')
      .mockResolvedValue({ ...structuredClone(catalog), restarting: true })
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Eventing' }))
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        '2 running sessions stop. Each one can be resumed after the restart.',
      ),
    ).toBeInTheDocument()
    expect(select).not.toHaveBeenCalled()
  })

  it('warns without a number when the reply leaves the count out', async () => {
    const select = vi.spyOn(modulesApi, 'select')
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    const { running_sessions: _, ...without } = structuredClone(catalog)
    vi.mocked(modulesApi.get).mockResolvedValue(without)
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Every running session stops. Each one can be resumed after the restart.',
      ),
    ).toBeInTheDocument()
    expect(select).not.toHaveBeenCalled()
  })

  it('cancelling the warning applies nothing', async () => {
    const select = vi.spyOn(modulesApi, 'select')
    vi.mocked(modulesApi.get).mockResolvedValue({
      ...structuredClone(catalog),
      running_sessions: 1,
    })
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        '1 running session stops. It can be resumed after the restart.',
      ),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(select).not.toHaveBeenCalled()
    // Opened from code, the dialog gives focus back to the button that asked.
    expect(screen.getByRole('button', { name: 'Apply 1 change' })).toHaveFocus()
  })

  it('warns that running sessions stop when the count cannot be read at Apply', async () => {
    const select = vi.spyOn(modulesApi, 'select')
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    vi.mocked(modulesApi.get).mockRejectedValue(new Error('offline'))
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Every running session stops. Each one can be resumed after the restart.',
      ),
    ).toBeInTheDocument()
    expect(select).not.toHaveBeenCalled()
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

it('explains Apply while module choices are loading', () => {
  vi.spyOn(modulesApi, 'get').mockImplementation(() => new Promise(() => {}))
  renderIntel(<ModulesSettings />)
  expect(
    screen.getByRole('button', { name: 'Apply' }),
  ).toHaveAccessibleDescription('Loading…')
})

it('explains Apply after a module read failure', async () => {
  vi.spyOn(modulesApi, 'get').mockRejectedValue(new Error('offline'))
  renderIntel(<ModulesSettings />)
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Apply' }),
    ).toHaveAccessibleDescription(
      'The module list could not be read. Try again.',
    ),
  )
})

it('explains Apply during saving and removes the idle reason when a choice changes', async () => {
  vi.spyOn(modulesApi, 'select').mockImplementation(() => new Promise(() => {}))
  const user = userEvent.setup()
  renderIntel(<ModulesSettings />)
  await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
  const apply = screen.getByRole('button', { name: 'Apply 1 change' })
  expect(apply).toBeEnabled()
  expect(apply).not.toHaveAttribute('aria-describedby')
  await user.click(apply)
  await waitFor(() => expect(apply).toBeDisabled())
  expect(apply).toHaveAccessibleDescription('Saving module selection…')
})

it('Turn on warns with the running-session count before the restart (#507)', async () => {
  vi.mocked(modulesApi.get).mockResolvedValue({
    ...structuredClone(catalog),
    running_sessions: 3,
  })
  const select = vi
    .spyOn(modulesApi, 'select')
    .mockResolvedValue({ ...structuredClone(catalog), restarting: true })
  const user = userEvent.setup()
  renderIntel(<TurnOnModule module="inventory" />)
  await user.click(screen.getByRole('button', { name: 'Turn on Inventory' }))
  const dialog = await screen.findByRole('dialog')
  expect(
    within(dialog).getByText(
      '3 running sessions stop. Each one can be resumed after the restart.',
    ),
  ).toBeInTheDocument()
  expect(select).not.toHaveBeenCalled()
  await user.click(
    within(dialog).getByRole('button', { name: 'Restart and apply' }),
  )
  await waitFor(() =>
    expect(select).toHaveBeenCalledWith(['finops', 'health', 'inventory']),
  )
})

it('Turn on applies at once when no session runs', async () => {
  const select = vi
    .spyOn(modulesApi, 'select')
    .mockResolvedValue({ ...structuredClone(catalog), restarting: true })
  const user = userEvent.setup()
  renderIntel(<TurnOnModule module="inventory" />)
  await user.click(screen.getByRole('button', { name: 'Turn on Inventory' }))
  await waitFor(() =>
    expect(select).toHaveBeenCalledWith(['finops', 'health', 'inventory']),
  )
  expect(screen.queryByRole('dialog')).toBeNull()
})

describe('when the restart does not end cleanly', () => {
  const restartingCatalog = () => ({
    ...structuredClone(catalog),
    restarting: true,
  })

  it('Turn on says the engine is still restarting after the wait gives up, and re-reads the modules anyway', async () => {
    vi.spyOn(modulesApi, 'select').mockResolvedValue(restartingCatalog())
    const queryClient = createTestQueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const user = userEvent.setup()
    renderIntel(<TurnOnModule module="inventory" />, { queryClient })
    await user.click(screen.getByRole('button', { name: 'Turn on Inventory' }))
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    await act(async () => restart.resolve(false))
    expect(
      await screen.findByText('Still restarting. Reload the page.'),
    ).toBeInTheDocument()
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.serverInfo })
  })

  it('Turn on still re-reads server-info when the page is left during the restart', async () => {
    vi.spyOn(modulesApi, 'select').mockResolvedValue(restartingCatalog())
    const queryClient = createTestQueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const user = userEvent.setup()
    const view = renderIntel(<TurnOnModule module="inventory" />, {
      queryClient,
    })
    await user.click(screen.getByRole('button', { name: 'Turn on Inventory' }))
    await screen.findByText('Restarting the engine…')
    view.unmount()
    expect(invalidate).not.toHaveBeenCalledWith({
      queryKey: queryKeys.serverInfo,
    })
    await act(async () => restart.resolve(true))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.serverInfo })
  })

  it('Apply says the engine is still restarting after the wait gives up', async () => {
    vi.spyOn(modulesApi, 'select').mockResolvedValue(restartingCatalog())
    const user = userEvent.setup()
    renderIntel(<ModulesSettings />)
    await user.click(await screen.findByRole('switch', { name: 'Inventory' }))
    await user.click(screen.getByRole('button', { name: 'Apply 1 change' }))
    await screen.findByText('Restarting the engine…')
    await act(async () => restart.resolve(false))
    expect(
      await screen.findByText('Still restarting. Reload the page.'),
    ).toBeInTheDocument()
  })
})

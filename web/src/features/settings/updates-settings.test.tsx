// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'

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
  UpdatesSettings,
  updatesApi,
  type EngineUpdate,
} from './updates-settings'

const available: EngineUpdate = {
  edition: 'community',
  version: '0.2',
  channel: 'stable',
  available: '0.3',
  update_available: true,
  released_at: '2026-10-16T00:00:00Z',
  notes: 'Adds the Updates page.\nFixes a typo.',
  security: true,
  advisories: ['CVE-2026-0001'],
  running_sessions: 2,
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('Settings > Updates', () => {
  it('names the running version, the newest release, its notes and the way to update', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(available)
    renderIntel(<UpdatesSettings />)
    expect(await screen.findByText('Update available')).toBeInTheDocument()
    const facts = screen.getByRole('list', { name: 'Release' })
    expect(within(facts).getByText('0.2')).toBeInTheDocument()
    expect(within(facts).getByText('0.3')).toBeInTheDocument()
    expect(within(facts).getByText('stable')).toBeInTheDocument()
    expect(within(facts).getByText('Community')).toBeInTheDocument()
    expect(screen.getByText(/Adds the Updates page\./)).toBeInTheDocument()
    expect(screen.getByText('CVE-2026-0001')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Update to 0.3' })).toBeEnabled()
  })

  it('offers nothing to install when the engine is on the newest release', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...available,
      version: '0.3',
      update_available: false,
      security: false,
      advisories: [],
    })
    renderIntel(<UpdatesSettings />)
    expect(await screen.findByText('Up to date')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Update to/ })).toBeNull()
  })

  it('keeps the running version and says why when the channel cannot be checked', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      edition: 'community',
      version: '0.2',
      channel: '',
      update_available: false,
      check_error: 'fetch manifest: 404 Not Found',
      running_sessions: 0,
    })
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findByText('fetch manifest: 404 Not Found'),
    ).toBeInTheDocument()
    expect(screen.getByText('Could not check for updates')).toBeInTheDocument()
    expect(screen.getByText('0.2')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Update to/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Check again' })).toBeEnabled()
  })

  it('says so when this engine cannot update from the console', async () => {
    vi.spyOn(updatesApi, 'get').mockRejectedValue(
      new ApiError(501, 'updates_unavailable', 'unavailable'),
    )
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findByText(
        'Updates cannot be installed from the console on this engine. Run olivares upgrade on the server.',
      ),
    ).toBeInTheDocument()
  })

  it('asks first, says how many sessions stop, installs, waits for the engine and reads the new version', async () => {
    const get = vi.spyOn(updatesApi, 'get').mockResolvedValue(available)
    const apply = vi
      .spyOn(updatesApi, 'apply')
      .mockResolvedValue({ ...available, restarting: true })
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Update to 0.3' }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/2 running sessions stop/),
    ).toBeInTheDocument()
    expect(apply).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: 'Install and restart' }),
    )
    await waitFor(() => expect(apply).toHaveBeenCalledTimes(1))
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    // Until the engine is back the page does not claim to be current, and does not offer
    // the install a second time.
    expect(screen.queryByText('Up to date')).toBeNull()
    expect(screen.getByRole('button', { name: 'Update to 0.3' })).toBeDisabled()
    const reads = get.mock.calls.length
    get.mockResolvedValue({
      ...available,
      version: '0.3',
      update_available: false,
    })
    await act(async () => restart.resolve(true))
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(reads))
    expect(await screen.findByText('Up to date')).toBeInTheDocument()
    expect(screen.queryByText('Restarting the engine…')).toBeNull()
  })

  it('shows the refusal and installs nothing when upgrade refuses', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(available)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new ApiError(
        409,
        'update_refused',
        'update refused: /usr/bin is not writable',
      ),
    )
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Update to 0.3' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Install and restart',
      }),
    )
    expect(
      await screen.findByText('update refused: /usr/bin is not writable'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Restarting the engine…')).toBeNull()
  })

  it('waits for the engine instead of calling it refused when the answer to an apply is lost', async () => {
    const get = vi.spyOn(updatesApi, 'get').mockResolvedValue(available)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new TypeError('Failed to fetch'),
    )
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Update to 0.3' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Install and restart',
      }),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    const reads = get.mock.calls.length
    await act(async () => restart.resolve(true))
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(reads))
  })

  it('says when the engine has not come back on the new version', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(available)
    vi.spyOn(updatesApi, 'apply').mockResolvedValue({
      ...available,
      restarting: true,
    })
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Update to 0.3' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Install and restart',
      }),
    )
    await screen.findByText('Restarting the engine…')
    await act(async () => restart.resolve(false))
    expect(
      await screen.findByText(/has not come back on the new version yet/),
    ).toBeInTheDocument()
  })

  it('says a newer binary is installed and waits for a restart', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...available,
      update_available: false,
      restart_required: true,
    })
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findByText(/Restart the engine with its service manager/),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Update to/ })).toBeNull()
  })
})

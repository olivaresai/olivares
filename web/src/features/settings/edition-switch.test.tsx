// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { toast } from '@/components/ui/toaster'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
const restart = vi.hoisted(() => ({
  resolve: (_ok: boolean) => {},
  probe: async (): Promise<boolean> => false,
}))
vi.mock('@/features/console/engine-restart', () => ({
  waitForEngineRestart: (opts: { probe: () => Promise<boolean> }) =>
    new Promise<boolean>((r) => {
      restart.resolve = r
      restart.probe = opts.probe
    }),
}))

import { EditionSwitch } from './edition-switch'
import {
  UpdatesSettings,
  updatesApi,
  type EngineUpdate,
} from './updates-settings'

// A Community build with a Business license: the newest release is the Business build of
// the SAME version, so the move is an edition, not a version.
const switching: EngineUpdate = {
  edition: 'community',
  version: '0.4',
  channel: 'stable',
  available: '0.4',
  update_available: true,
  running_sessions: 1,
  edition_switch: {
    to: 'business',
    install: 'binary',
    command: 'olivares upgrade --edition business',
    then: 'Then restart the service to run the Business build. Your data stays where it is.',
    console_action: true,
  },
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

describe('Switch to the licensed edition', () => {
  it('is one action: asks, switches, and waits for the EDITION to change at the same version', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    const apply = vi
      .spyOn(updatesApi, 'apply')
      .mockResolvedValue({ ...switching, restarting: true })
    const info = vi
      .spyOn(http, 'get')
      .mockResolvedValue({ version: '0.4', edition: 'community' })
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)

    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/1 running session stops/),
    ).toBeInTheDocument()
    expect(apply).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: 'Switch and restart' }),
    )
    await waitFor(() => expect(apply).toHaveBeenCalledTimes(1))
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    // The owner is told a SWITCH was installed, not a version update.
    expect(toast.success).toHaveBeenCalledWith('Switch installed.', undefined)

    // Same version, same edition: the engine has not come back on the Business build yet.
    expect(await restart.probe()).toBe(false)
    info.mockResolvedValue({ version: '0.4', edition: 'enterprise' })
    expect(await restart.probe()).toBe(true)

    await act(async () => restart.resolve(true))
    await waitFor(() =>
      expect(screen.queryByText('Restarting the engine…')).toBeNull(),
    )
  })

  it('says the restart is onto the licensed edition, not onto a new version', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      running_sessions: 0,
    })
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/restarts once on the licensed edition/),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText(/new version/)).toBeNull()
  })

  it('keeps waiting for the EDITION when the answer to the apply is lost', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new TypeError('Failed to fetch'),
    )
    const info = vi
      .spyOn(http, 'get')
      .mockResolvedValue({ version: '0.4', edition: 'community' })
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    // The old engine still answers as Community 0.4: not back yet. The licensed build does not.
    expect(await restart.probe()).toBe(false)
    info.mockResolvedValue({ version: '0.4', edition: 'enterprise' })
    expect(await restart.probe()).toBe(true)
  })

  it('keeps waiting when a proxy gives up on the apply with a 502', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new ApiError(502, 'bad_gateway', 'Bad Gateway'),
    )
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Bad Gateway')).toBeNull()
  })

  it('does not wait for a restart the engine itself refused with a 5xx', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new ApiError(
        503,
        'update_restart_unavailable',
        'this engine is not running as a server',
      ),
    )
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findByText('this engine is not running as a server'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Restarting the engine…')).toBeNull()
  })

  it('names Enterprise for an Enterprise license: one licensed build, the license names the edition', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      edition_switch: { ...switching.edition_switch!, to: 'enterprise' },
    })
    renderIntel(<EditionSwitch />)
    expect(
      await screen.findByRole('button', { name: 'Switch to Enterprise' }),
    ).toBeEnabled()
  })

  it('shows the exact command and no button where the engine cannot take the step', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      edition_switch: {
        to: 'business',
        install: 'package',
        command:
          'sudo olivares upgrade --edition business --target /usr/bin/olivares',
        then: 'Then restart the service.',
        console_action: false,
      },
    })
    renderIntel(<EditionSwitch />)
    expect(
      await screen.findByText(
        'sudo olivares upgrade --edition business --target /usr/bin/olivares',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('Then restart the service.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Switch to/ })).toBeNull()
  })

  it('names the manifest when there is no command', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      edition_switch: {
        to: 'enterprise',
        install: 'kubernetes',
        then: 'A pod is declarative: roll the Business image through your manifest.',
        console_action: false,
      },
    })
    renderIntel(<EditionSwitch />)
    expect(
      await screen.findByText(/roll the Business image through your manifest/),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Switch to/ })).toBeNull()
  })

  it('says why the check failed, in the words of the upgrade command, beside the step', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      update_available: false,
      check_error:
        'the licensed download needs a token: set OLIVARES_UPGRADE_TOKEN',
    })
    renderIntel(<EditionSwitch />)
    expect(
      await screen.findByText(/the licensed download needs a token/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Switch to Business' }),
    ).toBeEnabled()
  })

  it('renders nothing when there is no edition to switch to, or the engine does not answer', async () => {
    const get = vi.spyOn(updatesApi, 'get')
    get.mockResolvedValue({ ...switching, edition_switch: undefined })
    const { container, unmount } = renderIntel(<EditionSwitch />)
    await waitFor(() => expect(get).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
    unmount()

    get.mockRejectedValue(new Error('403'))
    const second = renderIntel(<EditionSwitch />)
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    expect(second.container).toBeEmptyDOMElement()
  })

  it('shows the engine its own refusal and installs nothing', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new ApiError(409, 'update_refused', 'update refused: no download token'),
    )
    const user = userEvent.setup()
    renderIntel(<EditionSwitch />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findByText('update refused: no download token'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Restarting the engine…')).toBeNull()
  })
})

describe('Settings > Updates on a Community build with a Business license', () => {
  it('offers the switch where it would offer a version', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    ).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^Update to/ })).toBeNull()
  })

  // The engine checks and installs the public channel where the licensed step cannot run (no
  // token, an unconnected mirror, a package): a security release is never held back by it.
  it('keeps the public update beside a step the engine cannot take', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      security: true,
      edition_switch: {
        ...switching.edition_switch!,
        then: 'Then restart the service. It needs a download token.',
        console_action: false,
      },
    })
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findByRole('button', { name: 'Update to 0.4' }),
    ).toBeEnabled()
    expect(
      screen.getByText('olivares upgrade --edition business'),
    ).toBeInTheDocument()
    expect(screen.getByText(/It needs a download token/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Switch to/ })).toBeNull()
  })

  it('calls the public Install an update, not a switch', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      edition_switch: { ...switching.edition_switch!, console_action: false },
    })
    vi.spyOn(updatesApi, 'apply').mockResolvedValue({
      ...switching,
      restarting: true,
    })
    vi.spyOn(http, 'get').mockResolvedValue({
      version: '0.4',
      edition: 'community',
    })
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Update to 0.4' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Install and restart',
      }),
    )
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Update installed.',
        undefined,
      ),
    )
    expect(toast.success).not.toHaveBeenCalledWith(
      'Switch installed.',
      undefined,
    )
  })

  it("shows the engine's refusal once on the page, not once per button", async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockRejectedValue(
      new ApiError(409, 'update_refused', 'update refused: no download token'),
    )
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findAllByText('update refused: no download token'),
    ).toHaveLength(1)
  })

  it('runs one update: Check stays off while the switch waits for the restart', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue(switching)
    vi.spyOn(updatesApi, 'apply').mockResolvedValue({
      ...switching,
      restarting: true,
    })
    vi.spyOn(http, 'get').mockResolvedValue({
      version: '0.4',
      edition: 'community',
    })
    const user = userEvent.setup()
    renderIntel(<UpdatesSettings />)
    await user.click(
      await screen.findByRole('button', { name: 'Switch to Business' }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Switch and restart',
      }),
    )
    expect(
      await screen.findByText('Restarting the engine…'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check again' })).toBeDisabled()
  })

  it('says why the check failed once, not twice', async () => {
    vi.spyOn(updatesApi, 'get').mockResolvedValue({
      ...switching,
      update_available: false,
      check_error:
        'the licensed download needs a token: set OLIVARES_UPGRADE_TOKEN',
    })
    renderIntel(<UpdatesSettings />)
    expect(
      await screen.findAllByText(/the licensed download needs a token/),
    ).toHaveLength(1)
  })
})

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { toastMock } = vi.hoisted(() => ({
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))
vi.mock('@/components/ui/toaster', () => ({ toast: toastMock }))

const { api, authState } = vi.hoisted(() => ({
  api: {
    listConsoleDestinations: vi.fn(),
    putConsoleDestination: vi.fn(),
    testConsoleDestination: vi.fn(),
    deleteConsoleDestination: vi.fn(),
  },
  authState: {
    can: (_p: string): boolean => true,
    activeTenant: 't1' as string | null,
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  notifyApi: api,
}))

import { ApiError } from '@/lib/api/errors'
import { DestinationsTab } from './destinations-tab'

const SECRET_URL = 'https://hooks.slack.com/services/T0/B0/SYNTHETICSECRET'

const consoleRow = {
  name: 'oncall',
  kind: 'slack',
  source: 'console' as const,
  live: true,
  webhook_host: 'hooks.slack.com',
}
const fileRow = {
  name: 'siem-splunk',
  kind: 'splunkhec',
  source: 'file' as const,
  live: true,
}
const kinds = [
  { kind: 'slack', title: 'Slack' },
  { kind: 'teams', title: 'Microsoft Teams' },
]

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listConsoleDestinations.mockResolvedValue({
    destinations: [consoleRow, fileRow],
    kinds,
    sealer_available: true,
  })
  api.putConsoleDestination.mockResolvedValue({ ...consoleRow })
  api.testConsoleDestination.mockResolvedValue({ ok: true })
  api.deleteConsoleDestination.mockResolvedValue({ ok: true })
})

describe('DestinationsTab', () => {
  it('asks a non-administrator to find one, without calling the engine', () => {
    authState.can = (permission) => permission !== 'system:admin'
    wrap(<DestinationsTab />)
    expect(
      screen.getByText(/destinations are managed by an administrator/i),
    ).toBeInTheDocument()
    expect(api.listConsoleDestinations).not.toHaveBeenCalled()
  })

  it('lists console and operator-file destinations; only console rows have actions; no URL is shown', async () => {
    wrap(<DestinationsTab />)
    expect(await screen.findByText('oncall')).toBeInTheDocument()
    expect(screen.getByText('siem-splunk')).toBeInTheDocument()
    expect(screen.getByText('hooks.slack.com')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /edit destination oncall/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /delete destination oncall/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /send a test message to oncall/i }),
    ).toBeInTheDocument()
    // The operator-file row is read-only: the console does not own it.
    expect(
      screen.queryByRole('button', { name: /destination siem-splunk/i }),
    ).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain('/services/')
  })

  it('warns when saving is impossible because no secret sealer is wired', async () => {
    api.listConsoleDestinations.mockResolvedValue({
      destinations: [],
      kinds,
      sealer_available: false,
    })
    wrap(<DestinationsTab />)
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /no secret sealer/i,
    )
  })

  it('adds a destination: the URL is typed into a masked field and sent once', async () => {
    api.listConsoleDestinations.mockResolvedValue({
      destinations: [],
      kinds,
      sealer_available: true,
    })
    const user = userEvent.setup()
    wrap(<DestinationsTab />)

    await user.click(
      (await screen.findAllByRole('button', { name: /add destination/i }))[0],
    )
    const dialog = await screen.findByRole('dialog')
    const url = within(dialog).getByLabelText(/webhook url/i)
    // Masked: the webhook URL is the secret and must not be readable on screen.
    expect(url).toHaveAttribute('type', 'password')
    const save = within(dialog).getByRole('button', { name: /^save$/i })
    expect(save).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/^name/i), 'Bad Name')
    await user.type(url, SECRET_URL)
    expect(save).toBeDisabled() // the engine's name rule is mirrored
    await user.clear(within(dialog).getByLabelText(/^name/i))
    await user.type(within(dialog).getByLabelText(/^name/i), 'oncall')
    expect(save).toBeEnabled()
    await user.click(save)

    await waitFor(() =>
      expect(api.putConsoleDestination).toHaveBeenCalledWith(
        { name: 'oncall', kind: 'slack', webhook_url: SECRET_URL },
        expect.anything(),
      ),
    )
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith(
        'Destination saved',
        undefined,
      ),
    )
    // The list is read again, so the new row (not the typed URL) is what shows.
    await waitFor(() =>
      expect(api.listConsoleDestinations).toHaveBeenCalledTimes(2),
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
  })

  it('says so when a saved destination did not open', async () => {
    api.putConsoleDestination.mockResolvedValue({ ...consoleRow, live: false })
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit destination oncall/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^save$/i }))
    await waitFor(() =>
      expect(toastMock.warning).toHaveBeenCalledWith(
        expect.stringMatching(/saved, but it is not open/i),
        undefined,
      ),
    )
  })

  it('edits without retyping the secret: a blank URL is not sent', async () => {
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit destination oncall/i }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText(/^name/i)).toBeDisabled()
    expect(within(dialog).getByLabelText(/webhook url/i)).toHaveValue('')
    await user.click(within(dialog).getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.putConsoleDestination).toHaveBeenCalled())
    const sent = api.putConsoleDestination.mock.calls[0][0]
    expect(sent).toEqual({ name: 'oncall', kind: 'slack' })
    expect('webhook_url' in sent).toBe(false)
  })

  it('sends a test message with the typed URL and saves nothing', async () => {
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      (await screen.findAllByRole('button', { name: /add destination/i }))[0],
    )
    const dialog = await screen.findByRole('dialog')
    const test = within(dialog).getByRole('button', {
      name: /send test message/i,
    })
    expect(test).toBeDisabled() // nothing to send yet
    await user.type(within(dialog).getByLabelText(/webhook url/i), SECRET_URL)
    await user.click(test)

    await waitFor(() =>
      expect(api.testConsoleDestination).toHaveBeenCalledWith(
        { name: '', kind: 'slack', webhook_url: SECRET_URL },
        expect.anything(),
      ),
    )
    expect(api.putConsoleDestination).not.toHaveBeenCalled()
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith(
        'Test message sent',
        undefined,
      ),
    )
  })

  it('reports a refused test message as the destination’s answer', async () => {
    api.testConsoleDestination.mockRejectedValue(
      new ApiError(422, 'notify_destination_test_failed', 'refused'),
    )
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      await screen.findByRole('button', {
        name: /send a test message to oncall/i,
      }),
    )
    await waitFor(() =>
      expect(toastMock.error).toHaveBeenCalledWith(
        expect.stringMatching(/did not accept the test message/i),
      ),
    )
    expect(api.testConsoleDestination).toHaveBeenCalledWith(
      { name: 'oncall', kind: 'slack' },
      expect.anything(),
    )
  })

  it('deletes a destination after confirmation', async () => {
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      await screen.findByRole('button', { name: /delete destination oncall/i }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(api.deleteConsoleDestination).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: /^delete destination$/i }),
    )
    await waitFor(() =>
      expect(api.deleteConsoleDestination).toHaveBeenCalledWith(
        'oncall',
        expect.anything(),
      ),
    )
  })

  it('still offers a kind when the dialog opens before the list has loaded', async () => {
    let release: (v: unknown) => void = () => {}
    api.listConsoleDestinations.mockReturnValue(
      new Promise((resolve) => {
        release = resolve
      }),
    )
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      (await screen.findAllByRole('button', { name: /add destination/i }))[0],
    )
    const dialog = await screen.findByRole('dialog')
    release({ destinations: [], kinds, sealer_available: true })

    await user.type(within(dialog).getByLabelText(/^name/i), 'oncall')
    await user.type(within(dialog).getByLabelText(/webhook url/i), SECRET_URL)
    await waitFor(() =>
      expect(
        within(dialog).getByRole('button', { name: /^save$/i }),
      ).toBeEnabled(),
    )
    await user.click(within(dialog).getByRole('button', { name: /^save$/i }))
    await waitFor(() =>
      expect(api.putConsoleDestination).toHaveBeenCalledWith(
        { name: 'oncall', kind: 'slack', webhook_url: SECRET_URL },
        expect.anything(),
      ),
    )
  })

  it('adds a Microsoft Teams destination and names its URL source in the hint', async () => {
    api.listConsoleDestinations.mockResolvedValue({
      destinations: [],
      kinds,
      sealer_available: true,
    })
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      (await screen.findAllByRole('button', { name: /add destination/i }))[0],
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('combobox', { name: /kind/i }))
    await user.click(
      await screen.findByRole('option', { name: 'Microsoft Teams' }),
    )
    expect(
      within(dialog).getByText(/workflows webhook url/i),
    ).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText(/^name/i), 'chat')
    await user.type(
      within(dialog).getByLabelText(/webhook url/i),
      'https://prod-12.westus.logic.azure.com:443/workflows/abc?sig=SYNTHETICSIG',
    )
    await user.click(within(dialog).getByRole('button', { name: /^save$/i }))
    await waitFor(() =>
      expect(api.putConsoleDestination).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'chat', kind: 'teams' }),
        expect.anything(),
      ),
    )
  })

  it('closes the delete dialog and reads the list again after a delete', async () => {
    const user = userEvent.setup()
    wrap(<DestinationsTab />)
    await user.click(
      await screen.findByRole('button', { name: /delete destination oncall/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^delete destination$/i }),
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(api.listConsoleDestinations).toHaveBeenCalledTimes(2),
    )
  })
})

/**
 * ONE ORANGE PER FIRST SCREEN (the regression's console rule 2026-10-17): with no
 * destination the intro row already carries "Add destination" in orange, so the
 * empty state offers the same verb in the secondary style.
 */
describe('DestinationsTab — one orange control on an empty list', () => {
  it('offers the empty-state verb in the secondary style; the header keeps the orange', async () => {
    api.listConsoleDestinations.mockResolvedValue({
      destinations: [],
      kinds,
      sealer_available: true,
    })
    wrap(<DestinationsTab />)
    const emptyState = (await screen.findByText('No destinations yet')).closest(
      '[data-slot="empty-state"]',
    ) as HTMLElement
    const inEmpty = within(emptyState).getByRole('button', {
      name: 'Add destination',
    })
    expect(inEmpty.className).not.toContain('bg-accent')
    const inHeader = screen
      .getAllByRole('button', { name: 'Add destination' })
      .find((button) => button !== inEmpty)
    expect(inHeader?.className).toContain('bg-accent')
  })
})

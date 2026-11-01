// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Cursor CLI signs in with its own `agent login`, which prints one link and ends when the
// browser approves: there is no code to type or to copy. The card offers the sign-in, the
// dialog shows the link and that it is waiting, and a person can cancel; no code field and
// no key form appear, because Cursor has neither.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

const LINK =
  'https://cursor.com/loginDeepControl?challenge=stub&uuid=00000000-0000-0000-0000-000000000000&mode=login&redirectTarget=cli'

const { api, auth, signIn } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    pins: vi.fn().mockResolvedValue({ pins: [] }),
    providers: vi.fn(() =>
      Promise.resolve({
        providers: [
          {
            instance: 'cursor',
            driver: 'cursor',
            default: true,
            config_dir: '/home/op/.cursor',
            state: 'not_signed_in',
            installed: true,
            limits: [],
            models: [],
            checked_at: '2026-10-23T10:00:00Z',
            source: 'agent status --format json',
          },
        ],
      }),
    ),
    plan: vi.fn(),
    install: vi.fn(),
    job: vi.fn(),
    detect: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    can: (p: string) => p === 'sessions:provider:write',
    activeTenant: null,
    principal: { user_id: 'root' },
  },
  signIn: { status: vi.fn(), start: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentToolsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
  listProviderKeys: () => Promise.resolve([]),
}))

import { useTenantStore } from '@/stores/tenant'
import { AgentToolsView } from './agent-tools-view'
import { SignIn } from '@/features/first-hour/first-hour'

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.clearAllMocks()
  api.inventory.mockResolvedValue({
    drivers: ['cursor'],
    inventory: {
      installed: [
        { driver: 'cursor', version: '2026.10.22-27dcf5f', state: 'installed' },
      ],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  signIn.status.mockImplementation((driver: string) =>
    Promise.resolve({ driver, installed: true, signed_in: false }),
  )
  const waiting = { id: 'si1', driver: 'cursor', state: 'waiting', url: LINK }
  signIn.start.mockResolvedValue(waiting)
  signIn.get.mockResolvedValue(waiting)
  signIn.cancel.mockResolvedValue(undefined)
})

it('offers Cursor CLI its own sign-in and relays the link without a code', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const card = await screen.findByTestId('tool-cursor')
  expect(within(card).getByText('Cursor CLI')).toBeInTheDocument()
  await userEvent.click(within(card).getByRole('button', { name: /^Sign in/ }))
  const dialog = await screen.findByRole('dialog', {
    name: /Sign in Cursor CLI/,
  })
  // The login is the organization's own (first-hour check): the start names it.
  await waitFor(() =>
    expect(signIn.start).toHaveBeenCalledWith('cursor', 'tenant-a'),
  )
  expect(
    await within(dialog).findByRole('link', { name: /open the sign-in page/i }),
  ).toHaveAttribute('href', LINK)
  expect(
    within(dialog).getByText(/Waiting for you to finish on the sign-in page/),
  ).toHaveAttribute('role', 'status')
  expect(
    within(dialog).queryByText(/The code works for 15 minutes/),
  ).not.toBeInTheDocument()
  expect(within(dialog).queryByTestId('device-code')).not.toBeInTheDocument()
  expect(within(dialog).queryByLabelText(/code/i)).not.toBeInTheDocument()
  expect(
    within(dialog).getByText('olivares tool login cursor'),
  ).toBeInTheDocument()
  // Cursor has no API-key way here: its account is its only credential.
  expect(
    within(dialog).queryByRole('button', { name: /api key/i, hidden: true }),
  ).not.toBeInTheDocument()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(signIn.cancel).toHaveBeenCalled())
})

it('keeps the Cursor sign-in open and shows an error when cancellation fails', async () => {
  signIn.cancel.mockRejectedValue(new Error('network down'))
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const card = await screen.findByTestId('tool-cursor')
  await userEvent.click(within(card).getByRole('button', { name: /^Sign in/ }))
  const dialog = await screen.findByRole('dialog', {
    name: /Sign in Cursor CLI/,
  })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'network down',
  )
  expect(
    within(dialog).getByRole('link', { name: /open the sign-in page/i }),
  ).toHaveAttribute('href', LINK)
  expect(
    within(dialog).getByText(/Waiting for you to finish on the sign-in page/),
  ).toBeInTheDocument()
})

it('scopes a named-profile cancellation for boundary cleanup', async () => {
  signIn.cancel.mockRejectedValue(new Error('network down'))
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SignIn
        driver="cursor"
        accountRef="ppf_cursor"
        mutationScope={['test', 'boundary']}
      />
    </QueryClientProvider>,
  )
  await userEvent.click(
    await screen.findByRole('button', { name: /^Sign in with your Cursor/ }),
  )
  await screen.findByText(/Waiting for you to finish on the sign-in page/)
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('network down')
  const cancelMutation = qc
    .getMutationCache()
    .getAll()
    .find((mutation) => mutation.options.mutationKey?.includes('cancel'))
  expect(cancelMutation?.options.mutationKey).toEqual([
    'test',
    'boundary',
    'cancel',
  ])
  expect(cancelMutation?.options.gcTime).toBe(0)
})

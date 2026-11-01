// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { authApi } from '@/lib/api/endpoints'
import { AcceptPairingPage } from './accept-pairing'

const state = vi.hoisted(() => ({ adopt: vi.fn(), navigate: vi.fn() }))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => state.navigate }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ adoptSession: state.adopt }),
}))
vi.mock('@/lib/api/endpoints', () => ({ authApi: { acceptPairing: vi.fn() } }))

beforeEach(() => {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/pair#pair=private-one-time-token')
})

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { mutations: { retry: false } } })
      }
    >
      <AcceptPairingPage />
    </QueryClientProvider>,
  )
}

it('removes the secret fragment and waits for explicit acceptance before adopting the cookie session', async () => {
  const session = {
    session_id: 'paired',
    csrf_token: 'csrf',
    expires_at: '2026-10-28T00:00:00Z',
  }
  vi.mocked(authApi.acceptPairing).mockResolvedValue(session)
  show()
  expect(window.location.hash).toBe('')
  expect(screen.queryByText(/private-one-time-token/)).not.toBeInTheDocument()
  expect(authApi.acceptPairing).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Pair this device' }),
  )
  await waitFor(() => expect(state.adopt).toHaveBeenCalledWith(session))
  expect(authApi.acceptPairing).toHaveBeenCalledWith('private-one-time-token')
  expect(state.navigate).toHaveBeenCalledWith({ to: '/', replace: true })
})

it('keeps refusals coarse and never adopts a failed pairing', async () => {
  vi.mocked(authApi.acceptPairing).mockRejectedValue(
    new Error('server refusal with private-one-time-token'),
  )
  show()
  await userEvent.click(
    screen.getByRole('button', { name: 'Pair this device' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'This link is missing, expired, or already used.',
  )
  expect(screen.queryByText(/private-one-time-token/)).not.toBeInTheDocument()
  expect(state.adopt).not.toHaveBeenCalled()
})

it('refuses a page without a pairing fragment', () => {
  window.history.replaceState(null, '', '/pair')
  show()
  expect(
    screen.getByRole('button', { name: 'Pair this device' }),
  ).toBeDisabled()
  expect(screen.getByRole('alert')).toBeInTheDocument()
})

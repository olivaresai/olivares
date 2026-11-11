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

// The page as drawn, like sign-in: the lockup, then ONE sheet holding the
// title, the line under it and the one filled button; a refused or missing
// link is a notice inside that sheet.
const sheetOf = (node: Element) => node.closest('[data-slot="auth-sheet"]')

it('holds the one h1 and its one filled button in one sheet', () => {
  show()
  const headings = screen.getAllByRole('heading', { level: 1 })
  expect(headings).toHaveLength(1)
  expect(document.querySelectorAll('[data-slot="auth-sheet"]')).toHaveLength(1)
  const sheet = sheetOf(headings[0])
  expect(sheet).not.toBeNull()
  const pair = screen.getByRole('button', { name: 'Pair this device' })
  const filled = sheet!.querySelectorAll('.bg-accent')
  expect(filled).toHaveLength(1)
  expect(filled[0]).toBe(pair)
  expect(pair).toHaveClass('w-full')
})

it('says a missing link in a notice inside the sheet', () => {
  window.history.replaceState(null, '', '/pair')
  show()
  const alert = screen.getByRole('alert')
  expect(alert).toHaveTextContent(
    'This link is missing, expired, or already used.',
  )
  expect(alert).toHaveAttribute('data-slot', 'notice')
  expect(alert).toHaveAttribute('data-tone', 'bad')
  expect(sheetOf(alert)).not.toBeNull()
})

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU2-06: a reload lost the sign-in in progress: the link and code were gone and only a new
// start was offered, which replaces the login the person was completing. The server still
// holds it; the row takes it up again.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, isSuperadmin: true }),
}))

import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { signInApi, type SignInFlow } from './api'
import { SignIn } from './first-hour'

let qc: QueryClient
function wrap(ui: ReactNode) {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const waiting: SignInFlow = {
  id: 'flow-1',
  driver: 'codex',
  state: 'waiting',
  url: 'https://auth.example.test/device',
  user_code: 'ABCD-1234',
}

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(signInApi, 'get').mockResolvedValue(waiting)
})

describe('a sign-in in progress, after a reload', () => {
  it('shows the link and code the server still holds, and starts nothing new', async () => {
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    const start = vi.spyOn(signInApi, 'start')
    wrap(<SignIn driver="codex" />)
    expect(await screen.findByText('ABCD-1234')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /open/i })).toHaveAttribute(
      'href',
      'https://auth.example.test/device',
    )
    expect(start).not.toHaveBeenCalled()
  })

  it('offers a fresh sign-in when the polled flow no longer exists', async () => {
    const user = userEvent.setup()
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    vi.mocked(signInApi.get).mockRejectedValue(
      new ApiError(
        404,
        'not_found',
        'This sign-in is no longer running. Start it again.',
      ),
    )
    const start = vi.spyOn(signInApi, 'start').mockResolvedValue({
      ...waiting,
      id: 'flow-2',
      user_code: 'WXYZ-5678',
    })
    wrap(<SignIn driver="codex" />)
    await screen.findByText('ABCD-1234')
    const restart = await screen.findByRole(
      'button',
      { name: /sign in/i },
      { timeout: 3000 },
    )
    expect(screen.getByRole('alert')).toHaveTextContent('Start it again.')
    expect(screen.queryByText('ABCD-1234')).toBeNull()
    await user.click(restart)
    expect(start).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('WXYZ-5678')).toBeInTheDocument()
  })

  it('keeps a live flow through a transient polling failure', async () => {
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    vi.mocked(signInApi.get)
      .mockRejectedValueOnce(
        new ApiError(503, 'unavailable', 'Try again later.'),
      )
      .mockResolvedValue({ ...waiting, user_code: 'WXYZ-5678' })
    wrap(<SignIn driver="codex" />)
    await screen.findByText('ABCD-1234')
    await waitFor(() => expect(signInApi.get).toHaveBeenCalledTimes(1), {
      timeout: 2500,
    })
    expect(screen.getByText('ABCD-1234')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(
      await screen.findByText('WXYZ-5678', {}, { timeout: 2500 }),
    ).toBeInTheDocument()
  })

  it('ignores a missing-flow reply after cancellation', async () => {
    const user = userEvent.setup()
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    let rejectPoll!: (err: Error) => void
    vi.mocked(signInApi.get).mockImplementation(
      () =>
        new Promise((_, reject) => {
          rejectPoll = reject
        }),
    )
    vi.spyOn(signInApi, 'cancel').mockResolvedValue({ ok: true })
    wrap(<SignIn driver="codex" />)
    await screen.findByText('ABCD-1234')
    await waitFor(() => expect(signInApi.get).toHaveBeenCalledTimes(1), {
      timeout: 2500,
    })
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    await act(async () =>
      rejectPoll(new ApiError(404, 'not_found', 'Start it again.')),
    )
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('ABCD-1234')).toBeNull()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  it('does not bring back a sign-in the person cancelled', async () => {
    const user = userEvent.setup()
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    const cancel = vi.spyOn(signInApi, 'cancel').mockResolvedValue({ ok: true })
    wrap(<SignIn driver="codex" />)
    await screen.findByText('ABCD-1234')
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    expect(cancel).toHaveBeenCalledWith('flow-1')
    expect(screen.queryByText('ABCD-1234')).toBeNull()
  })

  // SR4C on 1fb3bb20: a login started here, then seen as pending by a status refresh, came
  // back after Cancel from the cached status.
  it('does not bring back a login started here and then cancelled', async () => {
    const user = userEvent.setup()
    const status = vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
    })
    vi.spyOn(signInApi, 'start').mockResolvedValue(waiting)
    vi.spyOn(signInApi, 'cancel').mockResolvedValue({ ok: true })
    wrap(<SignIn driver="codex" />)
    await user.click(await screen.findByRole('button', { name: /sign in/i }))
    await screen.findByText('ABCD-1234')
    status.mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    await act(() => qc.refetchQueries())
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    expect(screen.queryByText('ABCD-1234')).toBeNull()
    await act(() => qc.refetchQueries())
    expect(screen.queryByText('ABCD-1234')).toBeNull()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  // SR4C on 58658d1e: resume A, cancel it, start B while the status still holds A, cancel B:
  // A came back with its old link and code.
  it('takes up a pending login once per row, not again after another start and cancel', async () => {
    const user = userEvent.setup()
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    vi.spyOn(signInApi, 'start').mockResolvedValue({
      ...waiting,
      id: 'flow-2',
      user_code: 'WXYZ-5678',
    })
    vi.spyOn(signInApi, 'get').mockImplementation(async (id: string) =>
      id === 'flow-2' ? { ...waiting, id, user_code: 'WXYZ-5678' } : waiting,
    )
    vi.spyOn(signInApi, 'cancel').mockResolvedValue({ ok: true })
    wrap(<SignIn driver="codex" />)
    await screen.findByText('ABCD-1234')
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    await user.click(screen.getByRole('button', { name: /sign in/i }))
    await screen.findByText('WXYZ-5678')
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    expect(screen.queryByText('ABCD-1234')).toBeNull()
    expect(screen.queryByText('WXYZ-5678')).toBeNull()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  // The same row signs in named accounts. The status it reads is the default account's, so an
  // account row never takes up that login.
  it("does not take up the default account's login on another account's row", async () => {
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: waiting,
    })
    wrap(<SignIn driver="codex" accountRef="acct-2" />)
    expect(
      await screen.findByRole('button', { name: /sign in/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText('ABCD-1234')).toBeNull()
  })

  it('offers a new start when the server holds none, as before', async () => {
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
    })
    wrap(<SignIn driver="codex" />)
    expect(
      await screen.findByRole('button', { name: /sign in/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText('ABCD-1234')).toBeNull()
  })
})

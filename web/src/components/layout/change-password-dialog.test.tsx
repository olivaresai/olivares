// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from 'sonner'
import { renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { actor: 'user:member', display_name: 'Member' },
    isSuperadmin: false,
    activeRole: 'viewer',
    logout: vi.fn(),
  }),
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: {} }),
}))
const post = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/client', () => ({ http: { post } }))
import { UserMenu } from './user-menu'
import { ChangePasswordDialog } from './change-password-dialog'

async function openForm() {
  const user = userEvent.setup()
  renderIntel(<UserMenu />)
  await user.click(screen.getByRole('button', { name: 'Account' }))
  await user.click(screen.getByRole('menuitem', { name: 'Change password' }))
  return user
}

async function submitForm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Current password'), 'current-secret')
  await user.type(screen.getByLabelText('New password'), 'replacement-secret')
  await user.type(
    screen.getByLabelText('Confirm new password'),
    'replacement-secret',
  )
  await user.click(screen.getByRole('button', { name: /^Change password$/ }))
}

function delayedPost() {
  let finish!: () => void
  post.mockReturnValueOnce(
    new Promise<void>((resolve) => {
      finish = resolve
    }),
  )
  return () => act(async () => finish())
}

describe('Own password from the account menu', () => {
  beforeEach(() => {
    post.mockReset()
    vi.spyOn(toast, 'success').mockImplementation(() => 'test-toast')
  })
  afterEach(() => {
    vi.restoreAllMocks()
    useSessionStore.getState().clear()
  })
  it('a viewer sends current and new password only, then clears the form', async () => {
    post.mockResolvedValue(undefined)
    const user = await openForm()
    expect(screen.getByLabelText('Current password')).toHaveAttribute(
      'autocomplete',
      'current-password',
    )
    expect(screen.getByLabelText('New password')).toHaveAttribute(
      'autocomplete',
      'new-password',
    )
    await user.type(screen.getByLabelText('Current password'), 'current-secret')
    await user.type(screen.getByLabelText('New password'), 'replacement-secret')
    await user.type(
      screen.getByLabelText('Confirm new password'),
      'replacement-secret',
    )
    await user.click(screen.getByRole('button', { name: /^Change password$/ }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/v1/account/password',
        {
          current_password: 'current-secret',
          new_password: 'replacement-secret',
        },
        expect.objectContaining({
          signal: expect.any(AbortSignal),
          dispatchGuard: expect.any(Function),
          sessionEffects: 'none',
        }),
      ),
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
  })
  it('confirmation mismatch refuses before the HTTP call', async () => {
    const user = await openForm()
    await user.type(screen.getByLabelText('Current password'), 'current-secret')
    await user.type(screen.getByLabelText('New password'), 'replacement-secret')
    await user.type(
      screen.getByLabelText('Confirm new password'),
      'different-secret',
    )
    await user.click(screen.getByRole('button', { name: /^Change password$/ }))
    expect(screen.getByRole('alert')).toHaveTextContent(
      'The passwords do not match.',
    )
    expect(post).not.toHaveBeenCalled()
  })
  it('shows the engine refusal, stays signed in and can retry', async () => {
    post
      .mockRejectedValueOnce(
        new ApiError(400, 'bad_request', 'The current password is incorrect.'),
      )
      .mockResolvedValueOnce(undefined)
    const user = await openForm()
    await user.type(screen.getByLabelText('Current password'), 'wrong-secret')
    await user.type(screen.getByLabelText('New password'), 'replacement-secret')
    await user.type(
      screen.getByLabelText('Confirm new password'),
      'replacement-secret',
    )
    await user.click(screen.getByRole('button', { name: /^Change password$/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The current password is incorrect.',
    )
    await user.clear(screen.getByLabelText('Current password'))
    await user.type(screen.getByLabelText('Current password'), 'current-secret')
    await user.click(screen.getByRole('button', { name: /^Change password$/ }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
  })
  it('closing and reopening leaves no password fields populated', async () => {
    const user = await openForm()
    await user.type(screen.getByLabelText('Current password'), 'current-secret')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('menuitem', { name: 'Change password' }))
    expect(screen.getByLabelText('Current password')).toHaveValue('')
    expect(screen.getByLabelText('New password')).toHaveValue('')
  })
  it('Cancel retires the request before the parent unmounts the dialog', async () => {
    const finish = delayedPost()
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    renderIntel(<ChangePasswordDialog onOpenChange={onOpenChange} />)
    await submitForm(user)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await finish()
    expect(post.mock.calls[0][2]?.signal?.aborted).toBe(true)
    expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
    expect(toast.success).not.toHaveBeenCalled()
  })
  it('late success after Cancel cannot close or clear a reopened form', async () => {
    const finish = delayedPost()
    const user = await openForm()
    await submitForm(user)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('menuitem', { name: 'Change password' }))
    await user.type(screen.getByLabelText('Current password'), 'fresh-secret')
    await finish()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByLabelText('Current password')).toHaveValue(
      'fresh-secret',
    )
    expect(toast.success).not.toHaveBeenCalled()
    expect(post).toHaveBeenCalledOnce()
  })
  it('replacement sign-in retires the request and ignores late success', async () => {
    useSessionStore.getState().setSession({
      csrfToken: 'synthetic-csrf-A',
      sessionId: 'synthetic-session-A',
      expiresAt: '2099-01-01T00:00:00Z',
    })
    const finish = delayedPost()
    const user = await openForm()
    await submitForm(user)
    act(() => {
      useSessionStore.getState().setSession({
        csrfToken: 'synthetic-csrf-B',
        sessionId: 'synthetic-session-B',
        expiresAt: '2099-01-01T00:00:00Z',
      })
    })
    await finish()
    expect(post.mock.calls[0][2]?.signal?.aborted).toBe(true)
    expect(toast.success).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(post).toHaveBeenCalledOnce()
  })
})

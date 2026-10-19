// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { SessionIdleSettings, sessionIdleApi } from './session-idle-settings'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    principal: { actor: 'user:admin' },
    isSuperadmin: true,
    can: () => true,
  }),
}))
afterEach(() => vi.restoreAllMocks())

it('starts off and saves one instance-wide idle window with privileged mutation', async () => {
  vi.spyOn(sessionIdleApi, 'get').mockResolvedValue({ idle_stop_minutes: 0 })
  const save = vi
    .spyOn(sessionIdleApi, 'save')
    .mockResolvedValue({ idle_stop_minutes: 6 })
  renderIntel(<SessionIdleSettings />)
  const input = await screen.findByLabelText('Stop after minutes in idle')
  expect(input).toHaveValue(0)
  await userEvent.clear(input)
  await userEvent.type(input, '6')
  await userEvent.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith(
      { idle_stop_minutes: 6 },
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    ),
  )
  expect(input).toHaveValue(6)
})

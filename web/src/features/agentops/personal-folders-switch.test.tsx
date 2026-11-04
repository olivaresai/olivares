// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// An administrator of folders sees whether each person gets "My files", on by
// default, and turning it off saves the choice the engine keeps.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, toast } = vi.hoisted(() => ({
  api: {
    personalFoldersSettings: vi.fn(),
    setPersonalFoldersSettings: vi.fn(),
  },
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./auth-boundary', () => ({
  useAuthBoundary: () => ({ epoch: 1 }),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))

import { PersonalFoldersSwitch } from './personal-folders-switch'

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PersonalFoldersSwitch />
    </QueryClientProvider>,
  )
}

describe('PersonalFoldersSwitch', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.personalFoldersSettings.mockResolvedValue({ enabled: true })
    // The engine keeps what was saved, so a refetch answers with it.
    api.setPersonalFoldersSettings.mockImplementation(
      async (enabled: boolean) => {
        api.personalFoldersSettings.mockResolvedValue({ enabled })
        return { enabled }
      },
    )
  })

  it('shows personal folders on and saves turning them off', async () => {
    const user = userEvent.setup()
    show()
    const box = await screen.findByRole('checkbox', {
      name: /personal folder/i,
    })
    expect(box).toBeChecked()
    await user.click(box)
    await waitFor(() =>
      expect(api.setPersonalFoldersSettings).toHaveBeenCalledWith(
        false,
        expect.anything(),
      ),
    )
    await waitFor(() => expect(box).not.toBeChecked())
  })
})
